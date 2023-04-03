// qemu-guest-kragent is a minimal QEMU Guest Agent implementation in
// Go so I can run Gokrazy appliance images in Proxmox in Qemu and
// can handle enough that Proxmox's web UI throws at the agent.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

var debug = flag.Bool("debug", false, "print debug messages")

func main() {
	flag.Parse()
	log.Printf("qemu-ga-go starting.")
	// TODO(bradfitz): look for the /dev/vport whose /sys/devices/pci0000\:00/0000\:00\:08.0/virtio2/virtio-ports/vport2p1/name is "org.qemu.guest_agent.0".
	// For now just hard-code what I see on gokrazy.
	pf, err := os.OpenFile("/dev/vport2p1", os.O_RDWR, 0666)
	if err != nil {
		log.Fatal(err)
	}
	defer pf.Close()
	fd := int(pf.Fd())
	pfReader := readAgentCharDevice(fd)

	bw := bufio.NewWriter(pf)
	je := json.NewEncoder(bw)

	d := json.NewDecoder(pfReader)
	for {
		var m Message
		if err := d.Decode(&m); err != nil {
			log.Fatal(err)
		}
		if *debug {
			log.Printf("Got message: %+v", m)
		}
		switch m.Execute {
		case "guest-sync-delimited":
			bw.WriteByte(0xff)
			je.Encode(Return{m.Arguments.ID})
			bw.Flush()
		case "guest-ping":
			je.Encode(Return{struct{}{}})
			bw.Flush()
		case "guest-network-get-interfaces":
			var iface []NetworkInterface
			ifs, _ := net.Interfaces()
			for _, goif := range ifs {
				nif := NetworkInterface{
					Name:            goif.Name,
					HardwareAddress: goif.HardwareAddr.String(),
				}
				addrs, _ := goif.Addrs()
				for _, addr := range addrs {
					nif.Addrs = append(nif.Addrs, wireAddrFromGo(addr))
				}
				iface = append(iface, nif)
			}
			je.Encode(Return{iface})
			bw.Flush()
		case "guest-shutdown":
			time.Sleep(time.Second / 4) // time for log message above to be written
			var err error
			switch m.Arguments.Mode {
			case "", "powerdown":
				err = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
			case "reboot":
				err = unix.Reboot(unix.LINUX_REBOOT_CMD_RESTART)
			case "halt":
				err = unix.Reboot(unix.LINUX_REBOOT_CMD_HALT)
			default:
				err = errors.New("invalid shutdown mode")
			}
			log.Printf("Reboot for mode %q = %v", m.Arguments.Mode, err)
		default:
			log.Printf("Unhandled command %q", m.Execute)
		}
	}
}

func readAgentCharDevice(fd int) io.Reader {
	if err := unix.SetNonblock(int(fd), false); err != nil {
		log.Fatal(err)
	}
	pr, pw := io.Pipe()
	go func() {
		epfd, err := unix.EpollCreate1(0)
		if err != nil {
			log.Fatalf("EpollCreate1: %v", err)
		}
		defer unix.Close(epfd)
		defer pw.CloseWithError(errors.New("reader ended"))

		// Note that it's important to use edge-triggered epoll here. When the
		// hypervisor host side of the virtio-serial is not present (or not
		// reading?), the epoll readability of our file descriptor is 17:
		// EPOLLIN & EPOLLHUP. With level-triggered epoll, we'd spin forever
		// because HUP means it's always readable. What we used to do (and the
		// official qemu-guest-agent seems to still do as of 2023-04-03) is just
		// spin but with a sleep in the middle. So it's the worst of both
		// worlds: you're still wasting CPU (but more slowly), and you now also
		// have latency from qemu agent queries from the host since you're stuck
		// in a sleep most the time, not blocking in an epoll_wait where we want
		// to be.  With this way, we block in epoll_wait, get a epoll_wait
		// event with "Events: 1" (readable) on our fd, then read it to exhaustion,
		// epoll_wait again, get "Events: 17" (readable + hup), read again,
		// get nothing (0, nil), and go back into epoll_wait. Here edge triggered
		// saves the day: it blocks until we change from IN|HUP to something else,
		// usually back to just readable (without HUP) and we continue.
		evt := unix.EpollEvent{
			Events: unix.EPOLLIN | unix.EPOLLET,
			Fd:     int32(fd),
		}
		if err := unix.EpollCtl(epfd, unix.EPOLL_CTL_ADD, fd, &evt); err != nil {
			log.Fatalf("EpollCtl: %v", err)
		}

		events := make([]unix.EpollEvent, 1)
		buf := make([]byte, 1024)
		for {
			n, err := unix.EpollWait(epfd, events, -1)
			if err != nil {
				log.Fatalf("EpollWait: %v", err)
			}
			if n == 0 {
				log.Fatalf("unexpected 0 epollwait")
			}
			if *debug {
				log.Printf("epoll_wait: %+v", events[0])
			}
			for {
				n, err := unix.Read(fd, buf)
				if *debug {
					var logBuf []byte
					if n >= 0 {
						logBuf = buf[:n]
					}
					log.Printf("read: (%v, %v): %q", n, err, logBuf)
				}
				if n > 0 {
					if _, err := pw.Write(buf[:n]); err != nil {
						log.Fatalf("pipe write: %v", err)
						return
					}
					continue
				}
				if err != nil {
					log.Fatalf("Read: %v", err)
				}
				break
			}
		}

	}()
	return pr
}

type Message struct {
	Execute   string      `json:"execute,omitempty"`
	Arguments MessageArgs `json:"arguments,omitempty"`
}

type Return struct {
	Val any `json:"return"`
}

type MessageArgs struct {
	ID   int64  `json:"id,omitempty"`
	Mode string `json:"mode"` // "halt" or "powerdown" (default) for "guest-shutdown"
}

type NetworkInterface struct {
	Name            string      `json:"name"`
	HardwareAddress string      `json:"hardware-address,omitempty"` // "00:00:00:00:00:00"
	Addrs           []IPAddress `json:"ip-addresses"`
}

type IPAddress struct {
	Type   string `json:"ip-address-type"` // "ipv4" or "ipv6"
	Addr   string `json:"ip-address"`      // "127.0.0.1"
	Prefix int    `json:"prefix"`          // 8
	// TODO: "statistics" (if it's useful)
}

func wireAddrFromGo(a net.Addr) (w IPAddress) {
	n, ok := a.(*net.IPNet)
	if ok {
		w.Addr = n.IP.String()
		if n.IP.To4() == nil {
			w.Type = "ipv6"
		} else {
			w.Type = "ipv4"
		}
		ones, _ := n.Mask.Size()
		w.Prefix = ones
	} else {
		w.Addr = a.String()
	}
	return w
}
