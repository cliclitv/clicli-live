package rtmp

import (
	"net"
	"time"

	"github.com/cliclitv/clicli-live/av"
	"github.com/cliclitv/clicli-live/container/flv"
	"github.com/cliclitv/clicli-live/protocol/rtmp/core"
	"github.com/cliclitv/clicli-live/utils/uid"
	"fmt"
	"net/url"

	"strings"

	"errors"

	"flag"

)

const (
	maxQueueNum = 1024
)

var (
	readTimeout  = flag.Int("readTimeout", 10, "read time out")
	writeTimeout = flag.Int("writeTimeout", 10, "write time out")
)

type Client struct {
	handler av.Handler
	getter  av.GetWriter
}

func NewRtmpClient(h av.Handler, getter av.GetWriter) *Client {
	return &Client{
		handler: h,
		getter:  getter,
	}
}

func (self *Client) Dial(url string, method string) error {
	connClient := core.NewConnClient()
	if err := connClient.Start(url, method); err != nil {
		return err
	}
	if method == av.PUBLISH {
		writer := NewVirWriter(connClient)
		self.handler.HandleWriter(writer)
	} else if method == av.PLAY {
		reader := NewVirReader(connClient)
		self.handler.HandleReader(reader)
		if self.getter != nil {
			writer := self.getter.GetWriter(reader.Info())
			self.handler.HandleWriter(writer)
		}
	}
	return nil
}

func (self *Client) GetHandle() av.Handler {
	return self.handler
}

type Server struct {
	handler av.Handler
	getters []av.GetWriter
}

func NewRtmpServer(h av.Handler, getters []av.GetWriter) *Server {
	return &Server{
		handler: h,
		getters: getters,
	}
}

func (self *Server) Serve(listener net.Listener) (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("rtmp serve panic: ", r)
		}
	}()

	for {
		var netconn net.Conn
		netconn, err = listener.Accept()
		if err != nil {
			return
		}
		conn := core.NewConn(netconn, 4*1024)
		fmt.Println("new client, connect remote:", conn.RemoteAddr().String(),
			"local:", conn.LocalAddr().String())
		go self.handleConn(conn)
	}
}

func (self *Server) handleConn(conn *core.Conn) error {
	if err := conn.HandshakeServer(); err != nil {
		conn.Close()
		fmt.Println("handleConn HandshakeServer err:", err)
		return err
	}
	connServer := core.NewConnServer(conn)

	if err := connServer.ReadMsg(); err != nil {
		conn.Close()
		fmt.Println("handleConn read msg err:", err)
		return err
	}
	if connServer.IsPublisher() {
		reader := NewVirReader(connServer)
		self.handler.HandleReader(reader)
		fmt.Println("new publisher: %+v", reader.Info())

		if len(self.getters) > 0 {
			for _, getter := range self.getters {
				writer := getter.GetWriter(reader.Info())
				if writer != nil {
					self.handler.HandleWriter(writer)
				}
			}
		}
	} else {
		writer := NewVirWriter(connServer)
		fmt.Println("new player: %+v", writer.Info())
		self.handler.HandleWriter(writer)
	}

	return nil
}

type GetInFo interface {
	GetInfo() (string, string, string)
}

type StreamReadWriteCloser interface {
	GetInFo
	Close(error)
	Write(core.ChunkStream) error
	Read(c *core.ChunkStream) error
}

type VirWriter struct {
	Uid    string
	closed bool
	av.RWBaser
	conn        StreamReadWriteCloser
	packetQueue chan av.Packet
}

func NewVirWriter(conn StreamReadWriteCloser) *VirWriter {
	ret := &VirWriter{
		Uid:         uid.NEWID(),
		conn:        conn,
		RWBaser:     av.NewRWBaser(time.Second * time.Duration(*writeTimeout)),
		packetQueue: make(chan av.Packet, maxQueueNum),
	}
	go ret.Check()
	go func() {
		err := ret.SendPacket()
		if err != nil {
			fmt.Println("send packet error: ", err)
		}
	}()
	return ret
}

func (self *VirWriter) Check() {
	var c core.ChunkStream
	for {
		if err := self.conn.Read(&c); err != nil {
			self.Close(err)
			return
		}
	}
}

func (self *VirWriter) DropPacket(pktQue chan av.Packet, info av.Info) {
	fmt.Errorf("[%v] packet queue max!!!", info)
	for i := 0; i < maxQueueNum-84; i++ {
		tmpPkt, ok := <-pktQue
		if ok {
			// try to don't drop audio
			if tmpPkt.IsAudio {
				if len(pktQue) > maxQueueNum-2 {
					fmt.Println("drop audio pkt")
					<-pktQue
				} else {
					pktQue <- tmpPkt
				}
			}

			if tmpPkt.IsVideo {
				videoPkt, ok := tmpPkt.Header.(av.VideoPacketHeader)
				// dont't drop sps config and dont't drop key frame
				if ok && (videoPkt.IsSeq() || videoPkt.IsKeyFrame()) {
					pktQue <- tmpPkt
				}
				if len(pktQue) > maxQueueNum-10 {
					fmt.Println("drop video pkt")
					<-pktQue
				}
			}
		}
	}
	fmt.Println("packet queue len: ", len(pktQue))
}

//
func (self *VirWriter) Write(p av.Packet) error {
	if !self.closed {
		if len(self.packetQueue) >= maxQueueNum-24 {
			self.DropPacket(self.packetQueue, self.Info())
		} else {
			self.packetQueue <- p
		}
		return nil
	} else {
		return errors.New("closed")
	}
}

func (self *VirWriter) SendPacket() error {
	var cs core.ChunkStream
	for {
		p, ok := <-self.packetQueue
		if ok {
			cs.Data = p.Data
			cs.Length = uint32(len(p.Data))
			cs.StreamID = 1
			cs.Timestamp = p.TimeStamp
			cs.Timestamp += self.BaseTimeStamp()

			if p.IsVideo {
				cs.TypeID = av.TAG_VIDEO
			} else {
				if p.IsMetadata {
					cs.TypeID = av.TAG_SCRIPTDATAAMF0
				} else {
					cs.TypeID = av.TAG_AUDIO
				}
			}

			self.SetPreTime()
			self.RecTimeStamp(cs.Timestamp, cs.TypeID)
			err := self.conn.Write(cs)
			if err != nil {
				self.closed = true
				return err
			}
		} else {
			return errors.New("closed")
		}

	}
	return nil
}

func (self *VirWriter) Info() (ret av.Info) {
	ret.UID = self.Uid
	ret.Type = "player"
	_, _, URL := self.conn.GetInfo()
	ret.URL = URL
	_url, err := url.Parse(URL)
	if err != nil {
		fmt.Println(err)
	}
	ret.Key = strings.TrimLeft(_url.Path, "/")
	return
}

func (self *VirWriter) Close(err error) {
	fmt.Println("player ", self.Info(), "closed: "+err.Error())
	if !self.closed {
		close(self.packetQueue)
	}
	self.closed = true
	self.conn.Close(err)
}

type VirReader struct {
	Uid string
	av.RWBaser
	demuxer *flv.Demuxer
	conn    StreamReadWriteCloser
}

func NewVirReader(conn StreamReadWriteCloser) *VirReader {
	return &VirReader{
		Uid:     uid.NEWID(),
		conn:    conn,
		RWBaser: av.NewRWBaser(time.Second * time.Duration(*writeTimeout)),
		demuxer: flv.NewDemuxer(),
	}
}

func (self *VirReader) Read(p *av.Packet) (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("rtmp read packet panic: ", r)
		}
	}()

	self.SetPreTime()
	var cs core.ChunkStream
	for {
		err = self.conn.Read(&cs)
		if err != nil {
			return err
		}
		if cs.TypeID == av.TAG_AUDIO ||
			cs.TypeID == av.TAG_VIDEO ||
			cs.TypeID == av.TAG_SCRIPTDATAAMF0 ||
			cs.TypeID == av.TAG_SCRIPTDATAAMF3 {
			break
		}
	}

	p.IsAudio = cs.TypeID == av.TAG_AUDIO
	p.IsVideo = cs.TypeID == av.TAG_VIDEO
	p.IsMetadata = (cs.TypeID == av.TAG_SCRIPTDATAAMF0 || cs.TypeID == av.TAG_SCRIPTDATAAMF3)
	p.Data = cs.Data
	p.TimeStamp = cs.Timestamp
	self.demuxer.DemuxH(p)
	return err
}

func (self *VirReader) Info() (ret av.Info) {
	ret.UID = self.Uid
	ret.Type = "publisher"
	_, _, URL := self.conn.GetInfo()
	ret.URL = URL
	_url, err := url.Parse(URL)
	if err != nil {
		fmt.Println(err)
	}
	ret.Key = strings.TrimLeft(_url.Path, "/")
	return
}

func (self *VirReader) Close(err error) {
	fmt.Println("publisher ", self.Info(), "closed: "+err.Error())
	self.conn.Close(err)
}
