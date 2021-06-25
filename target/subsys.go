package main

import (
	"encoding/json"
	"fmt"
	"math/bits"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/golang/glog"
	"github.com/neoul/open-gnmi/server"
	"github.com/spf13/pflag"
)

var sampleInterval = pflag.Duration("sample-interval", time.Second, "the sampling time of nic statistics in sec")

// IfStats - Interface statistics
type IfStats struct {
	Name           string
	Type           string
	Mtu            uint16
	Enabled        bool
	InetAddr       string
	Netmask        string
	Inet6Addr      string
	Inet6Prefixlen int
	Broadcast      string
	Ether          string
	RxPacket       uint64
	RxBytes        uint64
	RxError        uint64
	RxDrop         uint64
	RxOverruns     uint64
	RxFrame        uint64
	TxPacket       uint64
	TxBytes        uint64
	TxError        uint64
	TxDrop         uint64
	TxOverruns     uint64
	TxCarrier      uint64
	TxCollisions   uint64
}

func (ifstats *IfStats) MarshalJSON() ([]byte, error) {
	if ifstats == nil {
		return []byte("{}"), nil
	}
	var jsonstr strings.Builder
	jsonstr.WriteString("{")
	// config
	jsonstr.WriteString(fmt.Sprintf(`"name":%q,`, ifstats.Name))
	jsonstr.WriteString(fmt.Sprintf(`"config":{"name":%q,"enabled":%v,"mtu":%v},`, ifstats.Name, ifstats.Enabled, ifstats.Mtu))
	jsonstr.WriteString(fmt.Sprintf(`"state":{"name":%q,"enabled":%v,"mtu":%v`, ifstats.Name, ifstats.Enabled, ifstats.Mtu))
	jsonstr.WriteString(`,"counters":{`)
	jsonstr.WriteString(
		fmt.Sprintf(
			`"out-pkts":%d,"out-octets":%d,"out-errors":%d,"out-discards":%d`,
			ifstats.TxPacket,
			ifstats.TxBytes,
			ifstats.TxError,
			ifstats.TxDrop))
	jsonstr.WriteString(
		fmt.Sprintf(
			`,"in-pkts":%d,"in-octets":%d,"in-errors":%d,"in-discards":%d`,
			ifstats.TxPacket,
			ifstats.TxBytes,
			ifstats.TxError,
			ifstats.TxDrop))
	jsonstr.WriteString(`}`)
	jsonstr.WriteString(`}`)

	if ifstats.InetAddr != "" || ifstats.Inet6Addr != "" {
		subifstr := `,"subinterfaces":{"subinterface":{"0":{"index":0,"config":{"index": 0},`
		jsonstr.WriteString(subifstr)
		if ifstats.InetAddr != "" {
			inetstr := `"ipv4":{"addresses":{"address":{%q:{"ip":%q,"config":{"ip": %q,"prefix-length": %d},"state":{"ip": %q,"prefix-length": %d}}}}}`
			jsonstr.WriteString(fmt.Sprintf(inetstr, ifstats.InetAddr, ifstats.InetAddr, ifstats.InetAddr, mask2prefix(ifstats.Netmask), ifstats.InetAddr, mask2prefix(ifstats.Netmask)))
		}
		if ifstats.Inet6Addr != "" {
			if ifstats.InetAddr != "" {
				jsonstr.WriteString(`,`)
			}
			inetstr := `"ipv6":{"addresses":{"address":{%q:{"ip": %q,"config":{"ip":%q,"prefix-length": %d},"state":{"ip":%q,"prefix-length": %d}}}}}`
			jsonstr.WriteString(fmt.Sprintf(inetstr, ifstats.Inet6Addr, ifstats.Inet6Addr, ifstats.Inet6Addr, ifstats.Inet6Prefixlen, ifstats.Inet6Addr, ifstats.Inet6Prefixlen))
		}
		jsonstr.WriteString(`}}}`)
	}

	jsonstr.WriteString(`}`)
	fmt.Println(jsonstr.String())
	return []byte(jsonstr.String()), nil
}

// IfInfo - for NIC statistic
type IfInfo struct {
	Ifstats map[string]*IfStats
}

func split(s string) []string {
	ss := strings.Split(s, " ")
	ns := make([]string, 0, len(ss))
	for _, e := range ss {
		trimeds := strings.Trim(e, " \n")
		if trimeds != "" {
			ns = append(ns, trimeds)
		}
	}
	return ns
}

func mask2prefix(maskstr string) int {
	var prefix []uint8 = make([]uint8, 4)
	fmt.Sscanf(maskstr, "%d.%d.%d.%d", &prefix[0], &prefix[1], &prefix[2], &prefix[3])
	p := 0
	for i := range prefix {
		p += bits.TrailingZeros8(prefix[i])
	}
	return p
}

func newIfStats(ifinfo string) *IfStats {
	if ifinfo == "" {
		return nil
	}
	ifs := &IfStats{}
	defer func() {
		if r := recover(); r != nil {
			ifs = nil
			// fmt.Println("Recovered", r)
		}
	}()
	ifinfo = strings.Trim(ifinfo, " ")
	found := strings.Index(ifinfo, ": ")
	if found < 0 {
		return nil
	}

	ifs.Name = ifinfo[0:found]
	ifinfolist := strings.Split(ifinfo, "\n")
	mtustart := strings.Index(ifinfolist[0], "mtu")
	if mtustart >= 0 {
		fmt.Sscanf(ifinfolist[0][mtustart:], "mtu %d", &(ifs.Mtu))
	}
	for _, s := range ifinfolist[1:] {
		item := split(s)
		if len(item) == 0 {
			continue
		}
		switch item[0] {
		case "inet":
			ifs.InetAddr = item[1]
			ifs.Netmask = item[3]
		case "inet6":
			ifs.Inet6Addr = item[1]
			ifs.Inet6Prefixlen, _ = strconv.Atoi(item[3])
		case "ether":
			ifs.Ether = item[1]
		case "RX":
			if item[1] == "packets" {
				ifs.RxPacket, _ = strconv.ParseUint(item[2], 0, 64)
				ifs.RxBytes, _ = strconv.ParseUint(item[4], 0, 64)
			} else {
				ifs.RxError, _ = strconv.ParseUint(item[2], 0, 64)
				ifs.RxDrop, _ = strconv.ParseUint(item[4], 0, 64)
				ifs.RxOverruns, _ = strconv.ParseUint(item[6], 0, 64)
				ifs.RxFrame, _ = strconv.ParseUint(item[8], 0, 64)
			}
		case "TX":
			if item[1] == "packets" {
				ifs.TxPacket, _ = strconv.ParseUint(item[2], 0, 64)
				ifs.TxBytes, _ = strconv.ParseUint(item[4], 0, 64)
			} else {
				ifs.TxError, _ = strconv.ParseUint(item[2], 0, 64)
				ifs.TxDrop, _ = strconv.ParseUint(item[4], 0, 64)
				ifs.TxOverruns, _ = strconv.ParseUint(item[6], 0, 64)
				ifs.TxCarrier, _ = strconv.ParseUint(item[8], 0, 64)
				ifs.TxCollisions, _ = strconv.ParseUint(item[10], 0, 64)
			}
		}
	}
	// fmt.Println(*ifs)
	return ifs
}

func collectIfstats(name string) ([]*IfStats, error) {
	if name == "" {
		output, err := exec.Command("ifconfig").Output()
		if err != nil {
			return nil, err
		}
		iflist := strings.Split(string(output), "\n\n")
		ifstats := make([]*IfStats, 0, len(iflist))
		for _, ifentry := range iflist {
			if e := newIfStats(ifentry); e != nil {
				ifstats = append(ifstats, e)
			}
		}
		// fmt.Println(ifstats)
		if len(ifstats) > 0 {
			return ifstats, nil
		}
		return nil, fmt.Errorf("no nic found")
	}
	args := []string{name}
	output, err := exec.Command("ifconfig", args...).Output()
	if err != nil {
		return nil, err
	}
	ifentry := string(output)
	e := newIfStats(ifentry)
	if e != nil {
		return []*IfStats{e}, nil
	}
	return nil, fmt.Errorf("%q not found", name)
}

func pollingIfstats(s *server.Server, ifinfo *IfInfo, ticker *time.Ticker, done chan bool) {
	stats, _ := collectIfstats("") // collect all
	for _, entry := range stats {
		ifinfo.Ifstats[entry.Name] = entry
		b, err := json.Marshal(entry)
		if err != nil {
			glog.Errorf("error in marshaling ifstats: %v", err)
		}
		err = s.Write(fmt.Sprintf("/interfaces/interface[name=%s]", entry.Name), string(b))
		if err != nil {
			glog.Errorf("error in writing: %v", err)
		}
	}

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			stats, _ = collectIfstats("")
			for _, entry := range stats {
				ifinfo.Ifstats[entry.Name] = entry
				b, err := json.Marshal(entry)
				if err != nil {
					glog.Errorf("error in marshaling ifstats: %v", err)
				}
				err = s.Write(fmt.Sprintf("/interfaces/interface[name=%s]", entry.Name), string(b))
				if err != nil {
					glog.Errorf("error in writing: %v", err)
				}
			}
			b, _ := s.Root.MarshalJSON()
			fmt.Println(string(b))
		}
	}
}

// Subsystem() collects and populates the interface data to the gNMI target.
func Subsystem(s *server.Server) error {
	done := make(chan bool)
	ifinfo := &IfInfo{Ifstats: make(map[string]*IfStats)}
	var ticker *time.Ticker
	if *sampleInterval > 0 {
		ticker = time.NewTicker(*sampleInterval)
	} else {
		ticker = time.NewTicker(time.Second)
		ticker.Stop()
	}
	go pollingIfstats(s, ifinfo, ticker, done)
	return nil
}
