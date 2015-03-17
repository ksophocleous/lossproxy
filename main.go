package main

// This needs clean up

import (
	"fmt"
	"math/rand"
	"net"
	"os"
	"strconv"
	"time"
)

const udpBufferSize = 1500

type udpProxyReporter struct {
	loss       chan struct{}
	routed     chan struct{}
	packetInfo chan int
}

// UdpProxy is responsible for a single route of udp packets
type UdpProxy struct {
	Listen                 *net.UDPAddr
	Remote                 *net.UDPAddr
	Loss                   int32
	StartReporter          bool
	StartBandwidthReporter bool
	WaitChan               chan struct{}

	lc       *net.UDPConn
	packets  chan []byte
	shutdown bool
	reporter *udpProxyReporter
}

func (u *UdpProxy) stopProxy() error {
	u.shutdown = true
	u.lc.Close()
	return nil
}

func (u *UdpProxy) startListener() error {
	var err error
	u.lc, err = net.ListenUDP("udp", u.Listen)
	if err != nil {
		return err
	}

	u.packets = make(chan []byte, 100)
	u.reporter = &udpProxyReporter{}

	if u.StartBandwidthReporter {
		u.reporter.packetInfo = make(chan int, 100)
	}

	go func() {
		defer u.lc.Close()
		defer func() { close(u.packets) }()
		defer func() { close(u.reporter.packetInfo) }()

		b := make([]byte, udpBufferSize)
		for {
			read, _, err := u.lc.ReadFrom(b[:cap(b)])
			//fmt.Println(read)
			if err != nil {
				if u.shutdown {
					return
				}
				fmt.Println("Error on udp read: ", err)
				continue
			}
			if read > 0 {
				b = b[:read]
				if u.StartBandwidthReporter {
					u.reporter.packetInfo <- read
				}
				u.packets <- b
				b = make([]byte, udpBufferSize)
			}
		}
	}()

	return nil
}

func (u *UdpProxy) stopRouter() {
	u.lc.Close()
}

func (u *UdpProxy) startRouter() error {
	conn, err := net.DialUDP("udp", nil, u.Remote)
	if err != nil {
		return err
	}

	u.WaitChan = make(chan struct{})

	if u.StartReporter {
		u.reporter.loss = make(chan struct{}, 10)
		u.reporter.routed = make(chan struct{}, 10)
		go u.reporter.startReporter()
	}

	go func() {
		defer conn.Close()
		defer func() {
			close(u.WaitChan)
		}()
		for {
			data, ok := <-u.packets
			if ok == false {
				return
			}
			if len(data) > 0 {
				if u.reporter != nil {
					if rand.Int31n(100) < u.Loss {
						//fmt.Println("loss occured")
						u.reporter.loss <- struct{}{}
						continue
					}
					u.reporter.routed <- struct{}{}
				}
				//fmt.Println(conn.RemoteAddr())
				_, err := conn.Write(data)
				if err != nil {
					fmt.Printf("Send error: %s\n", err.Error())
				}
			}
		}
	}()

	return nil
}

func humanReadable(size float32) string {
	size = size * 8
	postfix := " bit/s"
	if size > 1000 {
		postfix = "kbit/s"
		size /= 1000
	}
	if size > 1000 {
		postfix = "mbit/s"
		size /= 1000
	}

	return fmt.Sprintf("%.2f%s", size, postfix)
}

// func (u *UdpProxy) startBandwidthReporter() {
// 	var totalSize int
// 	var count int

// 	lastTime := time.Now()
// 	timeout := time.After(1 * time.Second)

// 	for {
// 		select {

// 		case <-timeout:
// 			avg := float32(totalSize) / float32(time.Since(lastTime).Seconds())
// 			fmt.Printf("Avg: %s (%d)\n", humanReadable(avg), count)
// 			totalSize = 0
// 			count = 0
// 			lastTime = time.Now()
// 			timeout = time.After(1 * time.Second)
// 		}
// 	}
// }

func (u *udpProxyReporter) startReporter() {
	var lost uint32
	var good uint32
	var totalSize int
	var count int

	lastTime := time.Now()
	timeout := time.After(1 * time.Second)

	for {
		select {
		case size, ok := <-u.packetInfo:
			if ok == false {
				return
			}
			totalSize += size
			count++

		case _, ok := <-u.loss:
			if ok == false {
				return
			}
			lost++

		case _, ok := <-u.routed:
			if ok == false {
				return
			}
			good++

		case <-timeout:
			avg := float32(totalSize) / float32(time.Since(lastTime).Seconds())

			var perc float32
			if good > 0 {
				perc = (float32(lost) / float32(good+lost))
			} else {
				perc = 1
			}

			perc = perc * float32(time.Since(lastTime).Seconds()) * 100.0

			fmt.Printf("Loss: %5.1f -- Avg: %s (%d pkts)\n", perc, humanReadable(avg), count)

			totalSize = 0
			count = 0
			good = 0
			lost = 0
			lastTime = time.Now()
			timeout = time.After(1 * time.Second)
		}
	}
}

func (u *UdpProxy) wait() {
	<-u.WaitChan
}

func startUdpProxy(u *UdpProxy) error {
	fmt.Println("Starting UDP proxy")
	fmt.Printf("IN  <- From: %s\n", u.Listen.String())
	fmt.Printf("OUT -> To: %s\n", u.Remote.String())
	err := u.startListener()
	if err != nil {
		return err
	}
	err = u.startRouter()
	if err != nil {
		return err
	}
	return nil
}

func printUsage() {
	fmt.Println("usage is\n  lossproxy <listen_addr1> <remote_addr1> <loss1> [<listen_addr2> <remote_addr2> <loss2>] ...")
}

func parseAddr(addr string) (*net.UDPAddr, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	numPort, err := strconv.Atoi(port)
	if err != nil {
		return nil, err
	}

	u := &net.UDPAddr{
		IP:   net.ParseIP(host),
		Port: numPort,
	}

	return u, nil
}

func main() {
	routeInfo := os.Args[1:]

	if len(routeInfo) != 3 {
		printUsage()
		return
	}

	laAddr := routeInfo[0]
	raAddr := routeInfo[1]
	lossStr := routeInfo[2]

	la, err := parseAddr(laAddr)
	if err != nil {
		fmt.Printf("listen addr '%s' is invalid: %s\n", laAddr, err.Error())
		return
	}

	ra, err := parseAddr(raAddr)
	if err != nil {
		fmt.Printf("remote addr '%s' is invalid: %s\n", raAddr, err.Error())
		return
	}

	loss, err := strconv.ParseInt(lossStr, 10, 0)
	if err != nil {
		fmt.Printf("Packet loss is invalid '%s'", err.Error())
		return
	}

	if loss < 0 || loss > 100 {
		fmt.Printf("Packet loss %d is not valid... set to 0", loss)
		loss = 0
	}

	proxy := &UdpProxy{
		Listen: la,
		Remote: ra,
	}
	proxy.StartBandwidthReporter = true
	proxy.StartReporter = true
	proxy.Loss = int32(loss)

	err = startUdpProxy(proxy)
	if err != nil {
		fmt.Printf("Failed to start proxy for route: %s\n", err.Error())
		return
	}

	proxy.wait()
}
