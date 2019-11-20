package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"hash/fnv"
	"io"
	"net"
	"strconv"

	"golang.org/x/net/html/charset"

	pb "github.com/usher-2/u2ckdump/msg"
)

func UnmarshalContent(b []byte, v *TContent) error {
	buf := bytes.NewReader(b)
	decoder := xml.NewDecoder(buf)
	for {
		t, err := decoder.Token()
		if t == nil {
			if err != io.EOF {
				return err
			}
			break
		}
		switch _e := t.(type) {
		case xml.StartElement:
			switch _e.Name.Local {
			case "content":
				var (
					i   int
					err error
				)
				for _, _a := range _e.Attr {
					if _a.Name.Local == "id" {
						i, err = strconv.Atoi(_a.Value)
						if err != nil {
							return err
						}
						v.Id = int32(i)
					} else if _a.Name.Local == "entryType" {
						i, err = strconv.Atoi(_a.Value)
						if err != nil {
							return err
						}
						v.EntryType = int32(i)
					} else if _a.Name.Local == "urgencyType" {
						i, err = strconv.Atoi(_a.Value)
						if err != nil {
							return err
						}
						v.UrgencyType = int32(i)
					} else if _a.Name.Local == "includeTime" {
						v.IncludeTime = parseTime2(_a.Value)
					} else if _a.Name.Local == "blockType" {
						v.BlockType = _a.Value
					} else if _a.Name.Local == "hash" {
						v.Hash = _a.Value
					} else if _a.Name.Local == "ts" {
						v.Ts = parseTime(_a.Value)
					}
				}
			case "decision":
				err := decoder.DecodeElement(&v.Decision, &_e)
				if err != nil {
					return err
				}
			case "url":
				u := TXMLUrl{}
				err := decoder.DecodeElement(&u, &_e)
				if err != nil {
					return err
				}
				v.Url = append(v.Url, TUrl{Url: u.Url, Ts: parseTime(u.Ts)})
			case "domain":
				d := TXMLDomain{}
				err := decoder.DecodeElement(&d, &_e)
				if err != nil {
					return err
				}
				v.Domain = append(v.Domain, TDomain{Domain: d.Domain, Ts: parseTime(d.Ts)})
			case "ip":
				ip := TXMLIp{}
				err := decoder.DecodeElement(&ip, &_e)
				if err != nil {
					return err
				}
				v.Ip4 = append(v.Ip4, TIp4{Ip4: parseIp4(ip.Ip), Ts: parseTime(ip.Ts)})
			case "ipv6":
				ip := TXMLIp6{}
				err := decoder.DecodeElement(&ip, &_e)
				if err != nil {
					return err
				}
				v.Ip6 = append(v.Ip6, TIp6{Ip6: string(net.ParseIP(ip.Ip6)), Ts: parseTime(ip.Ts)})
			case "ipSubnet":
				s := TXMLSubnet{}
				err := decoder.DecodeElement(&s, &_e)
				if err != nil {
					return err
				}
				v.Subnet4 = append(v.Subnet4, TSubnet4{Subnet4: s.Subnet, Ts: parseTime(s.Ts)})
			case "ipv6Subnet":
				s := TXMLSubnet6{}
				err := decoder.DecodeElement(&s, &_e)
				if err != nil {
					return err
				}
				v.Subnet6 = append(v.Subnet6, TSubnet6{Subnet6: s.Subnet6, Ts: parseTime(s.Ts)})
			}
		}
	}
	return nil
}

func Parse(dumpFile io.Reader) error {
	var (
		err                            error
		stats                          Stats
		r                              TReg
		buffer                         bytes.Buffer
		bufferOffset, offsetCorrection int64
	)

	decoder := xml.NewDecoder(dumpFile)
	// we need this closure, we don't want constructor
	decoder.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		r, err := charset.NewReaderLabel(label, input)
		if err != nil {
			return nil, err
		}
		offsetCorrection = decoder.InputOffset()
		return io.TeeReader(r, &buffer), nil
	}
	SPass := make(IntSet, len(DumpSnap.Content)+1000)
	for {
		tokenStartOffset := decoder.InputOffset() - offsetCorrection
		t, err := decoder.Token()
		if t == nil {
			if err != io.EOF {
				return err
			}
			break
		}
		switch _e := t.(type) {
		case xml.StartElement:
			switch _e.Name.Local {
			case "register":
				handleRegister(_e, &r)
			case "content":
				id := getContentId(_e)
				// parse <content>...</content> only if need
				decoder.Skip()
				dif := tokenStartOffset - bufferOffset
				buffer.Next(int(dif))
				bufferOffset += dif
				tokenStartOffset = decoder.InputOffset() - offsetCorrection
				// create hash of <content>...</content> for comp
				tempBuf := buffer.Next(int(tokenStartOffset - bufferOffset))
				//u2Hash := crc32.Checksum(tempBuf, crc32Table)
				hash := fnv.New64a()
				hash.Write(tempBuf)
				u2Hash := hash.Sum64()
				bufferOffset = tokenStartOffset
				v := TContent{}
				// create or update
				DumpSnap.Lock()
				v0, exists := DumpSnap.Content[id]
				if !exists {
					err := UnmarshalContent(tempBuf, &v)
					if err != nil {
						Error.Printf("Decode Error: %s\n", err.Error())
					} else {
						v.Add(u2Hash, r.UpdateTime)
						stats.CntAdd++
					}
					SPass[v.Id] = NothingV
				} else if v0.U2Hash != u2Hash {
					err := UnmarshalContent(tempBuf, &v)
					if err != nil {
						Error.Printf("Decode Error: %s\n", err.Error())
					} else {
						v.Update(u2Hash, v0, r.UpdateTime)
						stats.CntUpdate++
					}
					SPass[v.Id] = NothingV
				} else {
					DumpSnap.Protobuf[id].RegistryUpdateTime = r.UpdateTime
					SPass[v0.Id] = NothingV
					//v = nil
				}
				DumpSnap.Unlock()
				stats.Cnt++
			}
		default:
			//fmt.printf("%v\n", _e)
		}
		dif := tokenStartOffset - bufferOffset
		buffer.Next(int(dif))
		bufferOffset += dif
	}
	// remove operations
	DumpSnap.Lock()
	for id, o2 := range DumpSnap.Content {
		if _, ok := SPass[id]; !ok {
			for _, v := range o2.Ip4 {
				DumpSnap.DeleteIp(v.Ip4, o2.Id)
			}
			for _, v := range o2.Ip6 {
				DumpSnap.DeleteIp6(string(v.Ip6), o2.Id)
			}
			for _, v := range o2.Subnet6 {
				DumpSnap.DeleteSubnet6(v.Subnet6, o2.Id)
			}
			for _, v := range o2.Subnet4 {
				DumpSnap.DeleteSubnet(v.Subnet4, o2.Id)
			}
			for _, v := range o2.Url {
				DumpSnap.DeleteUrl(NormalizeUrl(v.Url), o2.Id)
			}
			for _, v := range o2.Domain {
				DumpSnap.DeleteDomain(NormalizeDomain(v.Domain), o2.Id)
			}
			delete(DumpSnap.Content, id)
			delete(DumpSnap.Protobuf, id)
			stats.CntRemove++
		}
	}
	DumpSnap.utime = r.UpdateTime
	CntArrayIntSet := 0
	for _, a := range DumpSnap.ip {
		if CntArrayIntSet < len(a) {
			CntArrayIntSet = len(a)
		}
	}
	for _, a := range DumpSnap.ip6 {
		if CntArrayIntSet < len(a) {
			CntArrayIntSet = len(a)
		}
	}
	for _, a := range DumpSnap.subnet {
		if CntArrayIntSet < len(a) {
			CntArrayIntSet = len(a)
		}
	}
	for _, a := range DumpSnap.subnet6 {
		if CntArrayIntSet < len(a) {
			CntArrayIntSet = len(a)
		}
	}
	for _, a := range DumpSnap.url {
		if CntArrayIntSet < len(a) {
			CntArrayIntSet = len(a)
		}
	}
	for _, a := range DumpSnap.domain {
		if CntArrayIntSet < len(a) {
			CntArrayIntSet = len(a)
		}
	}
	DumpSnap.Unlock()
	Info.Printf("Records: %d Added: %d Updated: %d Removed: %d\n", stats.Cnt, stats.CntAdd, stats.CntUpdate, stats.CntRemove)
	Info.Printf("  IP: %d IPv6: %d Subnets: %d Subnets6: %d Domains: %d URSs: %d\n",
		len(DumpSnap.ip), len(DumpSnap.ip6), len(DumpSnap.subnet), len(DumpSnap.subnet6),
		len(DumpSnap.domain), len(DumpSnap.url))
	Info.Printf("Biggest array: %d\n", CntArrayIntSet)
	return err
}

func (v *TContent) Marshal() []byte {
	b, err := json.Marshal(v)
	if err != nil {
		Error.Printf("Error encoding: %s\n", err.Error())
	}
	return b
}

func (v *TContent) Update(u2Hash uint64, o *TMinContent, updateTime int64) {
	v1 := newMinContent(v.Id, u2Hash)
	v2 := newPbContent(v, updateTime)
	DumpSnap.Content[v.Id] = v1
	DumpSnap.Protobuf[v.Id] = v2
	v1.handleUpdateIp(v, o)
	v1.handleUpdateIp6(v, o)
	v1.handleUpdateSubnet(v, o)
	v1.handleUpdateSubnet6(v, o)
	v1.handleUpdateUrl(v, o)
	v1.handleUpdateDomain(v, o)
}

func (v *TContent) Add(u2Hash uint64, updateTime int64) {
	v1 := newMinContent(v.Id, u2Hash)
	v2 := newPbContent(v, updateTime)
	DumpSnap.Content[v.Id] = v1
	DumpSnap.Protobuf[v.Id] = v2
	v1.handleAddIp(v)
	v1.handleAddIp6(v)
	v1.handleAddSubnet6(v)
	v1.handleAddSubnet(v)
	v1.handleAddUrl(v)
	v1.handleAddDomain(v)
}

func (v *TMinContent) handleAddIp(v0 *TContent) {
	if len(v0.Ip4) > 0 {
		v.Ip4 = v0.Ip4
		for i, _ := range v.Ip4 {
			DumpSnap.AddIp(v.Ip4[i].Ip4, v.Id)
		}
	}
}

func (v *TMinContent) handleUpdateIp(v0 *TContent, o *TMinContent) {
	ipSet := make(map[uint32]Nothing, len(v.Ip4))
	if len(v0.Ip4) > 0 {
		v.Ip4 = v0.Ip4
		for i, _ := range v.Ip4 {
			DumpSnap.AddIp(v.Ip4[i].Ip4, v.Id)
			ipSet[v.Ip4[i].Ip4] = NothingV
		}
	}
	for i, _ := range o.Ip4 {
		ip := o.Ip4[i].Ip4
		if _, ok := ipSet[ip]; !ok {
			DumpSnap.DeleteIp(ip, o.Id)
		}
	}
}

func (v *TMinContent) handleAddDomain(v0 *TContent) {
	if len(v0.Domain) > 0 {
		v.Domain = v0.Domain
		for _, value := range v.Domain {
			domain := NormalizeDomain(value.Domain)
			DumpSnap.AddDomain(domain, v.Id)
		}
	}
}

func (v *TMinContent) handleUpdateDomain(v0 *TContent, o *TMinContent) {
	domainSet := NewStringSet(len(v.Domain))
	if len(v0.Domain) > 0 {
		v.Domain = v0.Domain
		for _, value := range v.Domain {
			domain := NormalizeDomain(value.Domain)
			DumpSnap.AddDomain(domain, v.Id)
			domainSet[domain] = NothingV
		}
	}
	for _, value := range o.Domain {
		domain := NormalizeDomain(value.Domain)
		if _, ok := domainSet[domain]; !ok {
			DumpSnap.DeleteDomain(domain, o.Id)
		}
	}
}

func (v *TMinContent) handleAddUrl(v0 *TContent) {
	if len(v0.Url) > 0 {
		v.Url = v0.Url
		for _, value := range v.Url {
			url := NormalizeUrl(value.Url)
			DumpSnap.AddUrl(url, v.Id)
			if url[:8] == "https://" {
				v0.HttpsBlock += 1
			}
		}
	}
}

func (v *TMinContent) handleUpdateUrl(v0 *TContent, o *TMinContent) {
	urlSet := NewStringSet(len(v.Url))
	if len(v0.Url) > 0 {
		v.Url = v0.Url
		for _, value := range v.Url {
			url := NormalizeUrl(value.Url)
			DumpSnap.AddUrl(url, v.Id)
			if url[:8] == "https://" {
				v0.HttpsBlock += 1
			}
			urlSet[url] = NothingV
		}
	}
	for _, value := range o.Url {
		url := NormalizeUrl(value.Url)
		if _, ok := urlSet[url]; !ok {
			DumpSnap.DeleteUrl(url, o.Id)
		}
	}
}

func (v *TMinContent) handleAddSubnet(v0 *TContent) {
	if len(v0.Subnet4) > 0 {
		v.Subnet4 = v0.Subnet4
		for _, value := range v.Subnet4 {
			DumpSnap.AddSubnet(value.Subnet4, v.Id)
		}
	}
}

func (v *TMinContent) handleUpdateSubnet(v0 *TContent, o *TMinContent) {
	subnetSet := NewStringSet(len(v.Subnet4))
	if len(v0.Subnet4) > 0 {
		v.Subnet4 = v0.Subnet4
		for _, value := range v.Subnet4 {
			DumpSnap.AddSubnet(value.Subnet4, v.Id)
			subnetSet[value.Subnet4] = NothingV
		}
	}
	for _, value := range o.Subnet4 {
		if _, ok := subnetSet[value.Subnet4]; !ok {
			DumpSnap.DeleteSubnet(value.Subnet4, o.Id)
		}
	}
}

func (v *TMinContent) handleAddSubnet6(v0 *TContent) {
	if len(v0.Subnet6) > 0 {
		v.Subnet6 = v0.Subnet6
		for _, value := range v.Subnet6 {
			DumpSnap.AddSubnet6(value.Subnet6, v.Id)
		}
	}
}

func (v *TMinContent) handleUpdateSubnet6(v0 *TContent, o *TMinContent) {
	subnet6Set := NewStringSet(len(v.Subnet6))
	if len(v0.Subnet6) > 0 {
		v.Subnet6 = v0.Subnet6
		for _, value := range v.Subnet6 {
			DumpSnap.AddSubnet(value.Subnet6, v.Id)
			subnet6Set[value.Subnet6] = NothingV
		}
	}
	for _, value := range o.Subnet6 {
		if _, ok := subnet6Set[value.Subnet6]; !ok {
			DumpSnap.DeleteSubnet6(value.Subnet6, o.Id)
		}
	}
}

func (v *TMinContent) handleAddIp6(v0 *TContent) {
	if len(v0.Ip6) > 0 {
		v.Ip6 = v0.Ip6
		for _, value := range v.Ip6 {
			DumpSnap.AddIp6(value.Ip6, v.Id)
		}
	}
}

func (v *TMinContent) handleUpdateIp6(v0 *TContent, o *TMinContent) {
	ip6Set := NewStringSet(len(v.Ip6))
	if len(v0.Ip6) > 0 {
		v.Ip6 = v0.Ip6
		for _, value := range v.Ip6 {
			DumpSnap.AddIp6(value.Ip6, v.Id)
			ip6Set[value.Ip6] = NothingV
		}
	}
	for _, value := range o.Ip6 {
		if _, ok := ip6Set[value.Ip6]; !ok {
			DumpSnap.DeleteIp6(value.Ip6, o.Id)
		}
	}
}

func getContentId(_e xml.StartElement) int32 {
	var (
		id  int
		err error
	)
	for _, _a := range _e.Attr {
		if _a.Name.Local == "id" {
			id, err = strconv.Atoi(_a.Value)
			if err != nil {
				Debug.Printf("Can't fetch id: %s: %s\n", _a.Value, err.Error())
			}
		}
	}
	return int32(id)
}

func handleRegister(_e xml.StartElement, r *TReg) {
	for _, _a := range _e.Attr {
		if _a.Name.Local == "formatVersion" {
			r.FormatVersion = _a.Value
		} else if _a.Name.Local == "updateTime" {
			r.UpdateTime = parseTime(_a.Value)
		} else if _a.Name.Local == "updateTimeUrgently" {
			r.UpdateTimeUrgently = _a.Value
		}
	}
}

func newMinContent(id int32, hash uint64) *TMinContent {
	return &TMinContent{Id: id, U2Hash: hash}
}

func newPbContent(v *TContent, utime int64) *pb.Content {
	v0 := pb.Content{}
	v0.Pack = v.Marshal()
	v0.RegistryUpdateTime = utime
	return &v0
}
