//go:build linux

package main

import (
	"net"
	"strings"
	"syscall"
	"unsafe"
)

// Info is the part of the kernel's struct tcp_info (getsockopt TCP_INFO) that
// the course uses. It is what `ss -ti` prints, read from inside the program.
type Info struct {
	State        string `json:"state"`
	Retransmits  uint8  `json:"retransmits"` // consecutive timeouts on the current segment
	Probes       uint8  `json:"probes"`      // zero-window or keepalive probes sent without answer
	Backoff      uint8  `json:"backoff"`     // RTO doublings in effect
	Options      uint8  `json:"options"`
	SndWscale    uint8  `json:"snd_wscale"`
	RcvWscale    uint8  `json:"rcv_wscale"`
	RTOus        uint32 `json:"rto_us"`
	SndMSS       uint32 `json:"snd_mss"`
	RcvMSS       uint32 `json:"rcv_mss"`
	Unacked      uint32 `json:"unacked"`
	Lost         uint32 `json:"lost"`
	RTTus        uint32 `json:"rtt_us"`
	RTTVarUs     uint32 `json:"rttvar_us"`
	Ssthresh     uint32 `json:"snd_ssthresh"`
	Cwnd         uint32 `json:"snd_cwnd"`
	PMTU         uint32 `json:"pmtu"`
	TotalRetrans uint32 `json:"total_retrans"`
}

const (
	optTimestamps    = 1
	optSACK          = 2
	optWscale        = 4
	optECN           = 8
	infiniteSsthresh = 0x7fffffff
)

var states = map[uint8]string{
	1: "ESTABLISHED", 2: "SYN_SENT", 3: "SYN_RECV", 4: "FIN_WAIT1", 5: "FIN_WAIT2", 6: "TIME_WAIT",
	7: "CLOSE", 8: "CLOSE_WAIT", 9: "LAST_ACK", 10: "LISTEN", 11: "CLOSING",
}

func tcpInfo(c *net.TCPConn) (Info, error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return Info{}, err
	}
	var ti syscall.TCPInfo
	var serr error
	err = rc.Control(func(fd uintptr) {
		l := uint32(unsafe.Sizeof(ti))
		_, _, e := syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, syscall.IPPROTO_TCP, syscall.TCP_INFO,
			uintptr(unsafe.Pointer(&ti)), uintptr(unsafe.Pointer(&l)), 0)
		if e != 0 {
			serr = e
		}
	})
	if err == nil {
		err = serr
	}
	if err != nil {
		return Info{}, err
	}
	// The byte after tcpi_options holds two 4-bit fields: snd_wscale (low), rcv_wscale (high).
	ws := ti.Pad_cgo_0[0]
	return Info{
		State: states[ti.State], Retransmits: ti.Retransmits, Probes: ti.Probes, Backoff: ti.Backoff,
		Options: ti.Options, SndWscale: ws & 0x0f, RcvWscale: ws >> 4,
		RTOus: ti.Rto, SndMSS: ti.Snd_mss, RcvMSS: ti.Rcv_mss, Unacked: ti.Unacked, Lost: ti.Lost,
		RTTus: ti.Rtt, RTTVarUs: ti.Rttvar, Ssthresh: ti.Snd_ssthresh, Cwnd: ti.Snd_cwnd,
		PMTU: ti.Pmtu, TotalRetrans: ti.Total_retrans,
	}, nil
}

// SsthreshString prints the slow-start threshold; "∞" means no loss yet, so
// the connection is still in its first slow start.
func (i Info) SsthreshString() string {
	if i.Ssthresh >= infiniteSsthresh {
		return "∞ (no loss seen yet)"
	}
	return strings.TrimSpace(itoa(i.Ssthresh) + " segments")
}

// OptionString lists what the handshake negotiated.
func (i Info) OptionString() string {
	var o []string
	if i.Options&optSACK != 0 {
		o = append(o, "sack")
	}
	if i.Options&optTimestamps != 0 {
		o = append(o, "ts")
	}
	if i.Options&optWscale != 0 {
		o = append(o, "wscale "+itoa(uint32(i.SndWscale))+"/"+itoa(uint32(i.RcvWscale)))
	}
	if i.Options&optECN != 0 {
		o = append(o, "ecn")
	}
	if len(o) == 0 {
		return "none"
	}
	return strings.Join(o, " ")
}

func itoa(u uint32) string {
	if u == 0 {
		return "0"
	}
	var b [10]byte
	n := len(b)
	for u > 0 {
		n--
		b[n] = byte('0' + u%10)
		u /= 10
	}
	return string(b[n:])
}

// congestion reads back the socket's congestion control algorithm.
func congestion(c *net.TCPConn) string {
	rc, err := c.SyscallConn()
	if err != nil {
		return "?"
	}
	var name [16]byte
	l := uint32(len(name))
	_ = rc.Control(func(fd uintptr) {
		_, _, _ = syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, syscall.IPPROTO_TCP, syscall.TCP_CONGESTION,
			uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&l)), 0)
	})
	return strings.TrimRight(string(name[:l]), "\x00")
}
