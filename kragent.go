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
	"syscall"
	"time"
)

var debug = flag.Bool("debug", false, "print debug messages")

func main() {
	flag.Parse()
	log.Printf("qemu-ga-go starting.")
	// TODO(bradfitz): look for the /dev/vport whose /sys/devices/pci0000\:00/0000\:00\:08.0/virtio2/virtio-ports/vport2p1/name is "org.qemu.guest_agent.0".
	// For now just hard-code what I see on gokrazy.
	pf, err := os.OpenFile("/dev/vport2p1", syscall.O_RDWR, 0666)
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
				err = syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
			case "reboot":
				err = syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
			case "halt":
				err = syscall.Reboot(syscall.LINUX_REBOOT_CMD_HALT)
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
	if err := syscall.SetNonblock(int(fd), false); err != nil {
		log.Fatal(err)
	}
	pr, pw := io.Pipe()
	go func() {
		epfd, err := syscall.EpollCreate1(0)
		if err != nil {
			log.Fatalf("EpollCreate1: %v", err)
		}
		defer syscall.Close(epfd)
		defer pw.CloseWithError(errors.New("reader ended"))

		evt := syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(fd)}
		if err := syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, fd, &evt); err != nil {
			log.Fatalf("EpollCtl: %v", err)
		}

		events := make([]syscall.EpollEvent, 1)
		buf := make([]byte, 1024)
		for {
			n, err := syscall.EpollWait(epfd, events, -1)
			if err != nil {
				log.Fatalf("EpollWait: %v", err)
			}
			if n == 0 {
				log.Fatalf("unexpected 0 epollwait")
			}
			for {
				n, err := syscall.Read(fd, buf)
				if n > 0 {
					if *debug {
						log.Printf("Read: (%v, %v): %q", n, err, buf[:n])
					}
					if _, err := pw.Write(buf[:n]); err != nil {
						log.Fatalf("pipe write: %v", err)
						return
					}
					continue
				}
				if err != nil {
					log.Fatalf("Read: %v", err)
				}
				// virtio-serial sucks and just spins with epoll saying it's readable
				// (well, EPOLLIN|EPOLLHUP) if nothing on the host has it open. So
				// just sleep so we don't burn CPU.
				if events[0].Events&syscall.EPOLLHUP != 0 {
					time.Sleep(time.Second)
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
