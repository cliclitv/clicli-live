package main

import (
	"flag"
	"net/url"
	"os/signal"
	"strings"
	"syscall"

	"os"
	"strconv"

	"time"

	"github.com/cliclitv/clicli-live/glog"
	"github.com/cliclitv/clicli-live/media/av"
	"github.com/cliclitv/clicli-live/media/container/flv"
	"github.com/cliclitv/clicli-live/media/protocol/rtmp"
	"github.com/cliclitv/clicli-live/media/utils/uid"
)

var (
	host      = flag.String("h", "rtmp://127.0.0.1/live/test", "rtmp server url")
	saveFlv   = flag.Bool("saveFlv", false, "enable save stream to flv file")
	clientNum = flag.Int("num", 1, "the client num")
)

func main() {
	flag.Parse()

	stream := rtmp.NewRtmpStream()
	rtmpClient := rtmp.NewRtmpClient(stream, nil)

	for i := 1; i <= *clientNum; i++ {
		play(rtmpClient, i)
		time.Sleep(200 * time.Millisecond)
	}

	catchSignal()
}

func play(rtmpClient *rtmp.Client, num int) {
	glog.Infof("dial to [%s]", *host)
	err := rtmpClient.Dial(*host, "play")
	if err != nil {
		glog.Errorf("dial [%s] error: %v", *host, err)
		return
	}
	if *saveFlv {
		info := parseURL(*host)
		paths := strings.Split(info.Key, "/")
		err := os.MkdirAll(paths[0], 0755)
		if err != nil {
			glog.Errorln(err)
			return
		}
		numStr := strconv.Itoa(num)
		filename := info.Key + "_" + numStr + ".flv"
		file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			glog.Errorln(err)
			return
		}
		flvWriter := flv.NewFLVWriter(paths[0], paths[1], *host, file)
		rtmpClient.GetHandle().HandleWriter(flvWriter)
		glog.Infof("save [%s] to [%s]\n\n", *host, filename)
	}
}

func parseURL(URL string) (ret av.Info) {
	ret.UID = uid.NEWID()
	ret.URL = URL
	_url, err := url.Parse(URL)
	if err != nil {
		glog.Errorln(err)
	}
	ret.Key = strings.TrimLeft(_url.Path, "/")
	ret.Inter = true
	return
}

func catchSignal() {
	sig := make(chan os.Signal)
	signal.Notify(sig, syscall.SIGINT)
	<-sig
	glog.Println("recieved signal!")
}
