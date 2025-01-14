package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"hash"
	"hash/fnv"
	"io"
	"net"
	"strconv"
	"strings"

	"golang.org/x/net/html/charset"

	"github.com/usher2/u2ckdump/internal/logger"
	pb "github.com/usher2/u2ckdump/msg"
)

const (
	elementContent   = "content"
	elementDecision  = "decision"
	elementURL       = "url"
	elementDomain    = "domain"
	elementIP4       = "ip"
	elementIP6       = "ipv6"
	elementIP4Subnet = "ipSubnet"
	elementIP6Subnet = "ipv6Subnet"
)

var hasher64 hash.Hash64

// UnmarshalContent - unmarshal <content> element.
func UnmarshalContent(contBuf []byte, content *Content) error {
	buf := bytes.NewReader(contBuf)
	decoder := xml.NewDecoder(buf)

	for {
		token, err := decoder.Token()
		if token == nil {
			if err != io.EOF {
				return fmt.Errorf("token: %w", err)
			}

			break
		}

		switch element := token.(type) {
		case xml.StartElement:
			// TODO: one func for one case, handle time parsing
			switch element.Name.Local {
			case elementContent:
				if err := parseContentElement(element, content); err != nil {
					return fmt.Errorf("parse content elm: %w", err)
				}
			case elementDecision:
				if err := decoder.DecodeElement(&content.Decision, &element); err != nil {
					return fmt.Errorf("parse decision elm: %w", err)
				}
			case elementURL:
				u := XMLURL{}
				if err := decoder.DecodeElement(&u, &element); err != nil {
					return fmt.Errorf("parse url elm: %w", err)
				}

				content.URL = append(content.URL, URL{URL: u.URL, Ts: parseRFC3339Time(u.Ts)})
			case elementDomain:
				domain := XMLDomain{}
				if err := decoder.DecodeElement(&domain, &element); err != nil {
					return fmt.Errorf("parse domain elm: %w", err)
				}

				content.Domain = append(content.Domain, Domain{Domain: domain.Domain, Ts: parseRFC3339Time(domain.Ts)})
			case elementIP4:
				ip4 := XMLIP{}
				if err := decoder.DecodeElement(&ip4, &element); err != nil {
					return fmt.Errorf("parse ip elm: %w", err)
				}

				content.IP4 = append(content.IP4, IP4{IP4: IPv4StrToInt(ip4.IP), Ts: parseRFC3339Time(ip4.Ts)})
			case elementIP6:
				ip6 := XMLIP6{}
				if err := decoder.DecodeElement(&ip6, &element); err != nil {
					return fmt.Errorf("parse ipv6 elm: %w", err)
				}

				content.IP6 = append(content.IP6, IP6{IP6: net.ParseIP(ip6.IP6), Ts: parseRFC3339Time(ip6.Ts)})
			case elementIP4Subnet:
				subnet4 := XMLSubnet{}
				if err := decoder.DecodeElement(&subnet4, &element); err != nil {
					return fmt.Errorf("parse subnet elm: %w", err)
				}

				content.Subnet4 = append(content.Subnet4, Subnet4{Subnet4: subnet4.Subnet, Ts: parseRFC3339Time(subnet4.Ts)})
			case elementIP6Subnet:
				subnet6 := XMLSubnet6{}
				if err := decoder.DecodeElement(&subnet6, &element); err != nil {
					return fmt.Errorf("parse ipv6 subnet elm: %w", err)
				}

				content.Subnet6 = append(content.Subnet6, Subnet6{Subnet6: subnet6.Subnet6, Ts: parseRFC3339Time(subnet6.Ts)})
			}
		}
	}

	return nil
}

// pasre <content> element itself.
func parseContentElement(element xml.StartElement, content *Content) error {
	for _, attr := range element.Attr {
		switch attr.Name.Local {
		case "id":
			id, err := strconv.Atoi(attr.Value)
			if err != nil {
				return fmt.Errorf("id atoi: %w: %s", err, attr.Value)
			}

			content.ID = int32(id)
		case "entryType":
			entryType, err := strconv.Atoi(attr.Value)
			if err != nil {
				return fmt.Errorf("entryType atoi: %w: %s", err, attr.Value)
			}

			content.EntryType = int32(entryType)
		case "urgencyType":
			urgencyType, err := strconv.Atoi(attr.Value)
			if err != nil {
				return fmt.Errorf("urgencyType atoi: %w: %s", err, attr.Value)
			}

			content.UrgencyType = int32(urgencyType)
		case "includeTime":
			content.IncludeTime = parseMoscowTime(attr.Value)
		case "blockType":
			content.BlockType = attr.Value
		case "hash":
			content.Hash = attr.Value
		case "ts":
			content.Ts = parseRFC3339Time(attr.Value)
		}
	}

	return nil
}

// Parse - parse dump.
func Parse(dumpFile io.Reader) error {
	var (
		reg                            Reg
		buffer                         bytes.Buffer
		bufferOffset, offsetCorrection int64

		stats ParseStatistics
	)

	hasher64 = fnv.New64a()
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

	// TODO: What is it?
	ContJournal := make(Int32Map, len(CurrentDump.ContentIdx))

	for {
		tokenStartOffset := decoder.InputOffset() - offsetCorrection

		token, err := decoder.Token()
		if token == nil {
			if err != io.EOF {
				return err
			}

			break
		}

		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "register":
				parseRegister(element, &reg)
			case "content":
				id := getContentId(element)

				// parse <content>...</content> only if need
				decoder.Skip()

				// read buffer to mark anyway
				diff := tokenStartOffset - bufferOffset
				buffer.Next(int(diff))
				bufferOffset += diff

				// calc end of element
				tokenStartOffset = decoder.InputOffset() - offsetCorrection

				// create hash of <content>...</content> for comp
				contBuf := buffer.Next(int(tokenStartOffset - bufferOffset))
				if stats.MaxContentSize < len(contBuf) {
					stats.MaxContentSize = len(contBuf)
				}

				bufferOffset = tokenStartOffset

				hasher64.Reset()
				hasher64.Write(contBuf)

				newRecordHash := hasher64.Sum64()

				// create or update
				CurrentDump.Lock()

				prevCont, exists := CurrentDump.ContentIdx[id]
				ContJournal[id] = Nothing{} // add to journal.

				switch {
				case !exists:
					newCont, err := NewContent(newRecordHash, contBuf)
					if err != nil {
						logger.Error.Printf("Decode Error: %s\n", err)

						break
					}

					CurrentDump.NewPackedContent(newCont, reg.UpdateTime)
					stats.AddCount++
				case prevCont.RecordHash != newRecordHash:
					newCont, err := NewContent(newRecordHash, contBuf)
					if err != nil {
						logger.Error.Printf("Decode Error: %s\n", err)

						break
					}

					CurrentDump.MergePackedContent(newCont, prevCont, reg.UpdateTime)
					stats.UpdateCount++
				default:
					CurrentDump.SetContentUpdateTime(id, reg.UpdateTime)
				}

				CurrentDump.Unlock()
				stats.Count++
			}
		}

		// read buffer anyway
		diff := tokenStartOffset - bufferOffset
		buffer.Next(int(diff))
		bufferOffset += diff
	}

	// Cleanup.
	CurrentDump.Cleanup(ContJournal, &stats, reg.UpdateTime)

	stats.Update()
	Stats = stats

	// Print stats.

	logger.Info.Printf("Records: %d Added: %d Updated: %d Removed: %d\n", stats.Count, stats.AddCount, stats.UpdateCount, stats.RemoveCount)
	logger.Info.Printf("  IP: %d IPv6: %d Subnets: %d Subnets6: %d Domains: %d URSs: %d\n",
		len(CurrentDump.ip4Idx), len(CurrentDump.ip6Idx), len(CurrentDump.subnet4Idx), len(CurrentDump.subnet6Idx),
		len(CurrentDump.domainIdx), len(CurrentDump.urlIdx))
	logger.Info.Printf("Biggest array: %d\n", stats.MaxIDSetLen)
	logger.Info.Printf("Biggest content: %d\n", stats.MaxContentSize)

	return nil
}

func NewContent(recordHash uint64, buf []byte) (*Content, error) {
	content := &Content{
		RecordHash: recordHash,
	}

	err := UnmarshalContent(buf, content)
	if err != nil {
		return nil, err
	}

	return content, nil
}

func (dump *Dump) Cleanup(existed Int32Map, stats *ParseStatistics, utime int64) {
	dump.Lock()
	defer dump.Unlock()

	dump.purge(existed, stats)   // remove deleted records from index.
	dump.calcMaxEntityLen(stats) // calc max entity len.
	dump.utime = utime           // set global update time.
}

func (dump *Dump) calcMaxEntityLen(stats *ParseStatistics) {
	stats.MaxIDSetLen = 0

	for _, a := range dump.ip4Idx {
		if stats.MaxIDSetLen < len(a) {
			stats.MaxIDSetLen = len(a)
		}
	}
	for _, a := range dump.ip6Idx {
		if stats.MaxIDSetLen < len(a) {
			stats.MaxIDSetLen = len(a)
		}
	}
	for _, a := range dump.subnet4Idx {
		if stats.MaxIDSetLen < len(a) {
			stats.MaxIDSetLen = len(a)
		}
	}
	for _, a := range dump.subnet6Idx {
		if stats.MaxIDSetLen < len(a) {
			stats.MaxIDSetLen = len(a)
		}
	}
	for _, a := range dump.urlIdx {
		if stats.MaxIDSetLen < len(a) {
			stats.MaxIDSetLen = len(a)
		}
	}
	for _, a := range dump.domainIdx {
		if stats.MaxIDSetLen < len(a) {
			stats.MaxIDSetLen = len(a)
		}
	}
}

// purge - remove deleted records from index.
func (dump *Dump) purge(existed Int32Map, stats *ParseStatistics) {
	for id, cont := range dump.ContentIdx {
		if _, ok := existed[id]; !ok {
			for _, ip4 := range cont.IP4 {
				dump.RemoveFromIndexIP4(ip4.IP4, cont.ID)
			}

			for _, ip6 := range cont.IP6 {
				ip6 := string(ip6.IP6)
				dump.RemoveFromIndexIP6(ip6, cont.ID)
			}

			for _, subnet6 := range cont.Subnet6 {
				dump.RemoveFromIndexSubnet6(subnet6.Subnet6, cont.ID)
			}

			for _, subnet4 := range cont.Subnet4 {
				dump.RemoveFromSubnet4(subnet4.Subnet4, cont.ID)
			}

			for _, u := range cont.URL {
				dump.RemoveFromIndexURL(NormalizeURL(u.URL), cont.ID)
			}

			for _, domain := range cont.Domain {
				dump.RemoveFromIndexDomain(NormalizeDomain(domain.Domain), cont.ID)
			}

			dump.RemoveFromIndexDecision(cont.Decision, cont.ID)

			delete(dump.ContentIdx, id)

			stats.RemoveCount++
		}
	}
}

// Marshal - encodes content to JSON.
func (record *Content) Marshal() []byte {
	b, err := json.Marshal(record)
	if err != nil {
		logger.Error.Printf("Error encoding: %s\n", err.Error())
	}
	return b
}

// constructBlockType - returns block type for content.
func (record *Content) constructBlockType() int32 {
	switch record.BlockType {
	case "ip":
		return BlockTypeIP
	case "domain":
		return BlockTypeDomain
	case "domain-mask":
		return BlockTypeMask
	default:
		if record.BlockType != "default" && record.BlockType != "" {
			logger.Error.Printf("Unknown block type: %s\n", record.BlockType)
		}
		if record.HTTPSBlock == 0 {
			return BlockTypeURL
		} else {
			return BlockTypeHTTPS
		}
	}
}

func (dump *Dump) SetContentUpdateTime(id int32, updateTime int64) {
	dump.ContentIdx[id].RegistryUpdateTime = dump.utime
}

// MergePackedContent - merges new content with previous one.
// It is used to update existing content.
func (dump *Dump) MergePackedContent(record *Content, prev *PackedContent, updateTime int64) {
	prev.refreshPackedContent(record.RecordHash, updateTime, record.Marshal())

	dump.EctractAndApplyUpdateIP4(record, prev)
	dump.EctractAndApplyUpdateIP6(record, prev)
	dump.EctractAndApplyUpdateSubnet4(record, prev)
	dump.EctractAndApplyUpdateSubnet6(record, prev)
	dump.EctractAndApplyUpdateDomain(record, prev)
	dump.EctractAndApplyUpdateURL(record, prev)
	dump.EctractAndApplyUpdateDecision(record, prev) // reason for ALARM!!!
}

// NewPackedContent - creates new content.
// It is used to add new content.
func (dump *Dump) NewPackedContent(record *Content, updateTime int64) {
	fresh := newPackedContent(record.ID, record.RecordHash, updateTime, record.Marshal())
	dump.ContentIdx[record.ID] = fresh

	dump.ExtractAndApplyIP4(record, fresh)
	dump.ExtractAndApplyIP6(record, fresh)
	dump.ExtractAndApplySubnet4(record, fresh)
	dump.ExtractAndApplySubnet6(record, fresh)
	dump.ExtractAndApplyDomain(record, fresh)
	dump.ExtractAndApplyURL(record, fresh)
	dump.ExtractAndApplyDecision(record, fresh)
}

func (dump *Dump) ExtractAndApplyDecision(record *Content, pack *PackedContent) {
	pack.Decision = hashDecision(&record.Decision)
	dump.InsertToIndexDecision(pack.Decision, pack.ID)
}

// IT IS REASON FOR ALARM!!!!
func (dump *Dump) EctractAndApplyUpdateDecision(record *Content, pack *PackedContent) {
	dump.RemoveFromIndexDecision(pack.Decision, pack.ID)

	pack.Decision = hashDecision(&record.Decision)

	dump.InsertToIndexDecision(pack.Decision, pack.ID)
}

func hashDecision(decision *Decision) uint64 {
	// hash.Write([]byte(v0.Decision.Org + " " + v0.Decision.Number + " " + v0.Decision.Date))
	hasher64.Reset()
	hasher64.Write([]byte(decision.Org))
	hasher64.Write([]byte(" "))
	hasher64.Write([]byte(decision.Number))
	hasher64.Write([]byte(" "))
	hasher64.Write([]byte(decision.Date))
	return hasher64.Sum64()
}

func (dump *Dump) ExtractAndApplyIP4(record *Content, pack *PackedContent) {
	if len(record.IP4) > 0 {
		pack.IP4 = record.IP4
		for _, ip4 := range pack.IP4 {
			dump.InsertToIndexIP4(ip4.IP4, pack.ID)
		}
	}
}

func (dump *Dump) EctractAndApplyUpdateIP4(record *Content, pack *PackedContent) {
	ipExisted := make(map[uint32]Nothing, len(pack.IP4))
	if len(record.IP4) > 0 {
		for _, ip4 := range record.IP4 {
			pack.InsertIP4(ip4)
			dump.InsertToIndexIP4(ip4.IP4, pack.ID)
			ipExisted[ip4.IP4] = Nothing{}
		}
	}

	for _, ip4 := range pack.IP4 {
		if _, ok := ipExisted[ip4.IP4]; !ok {
			pack.RemoveIP4(ip4)
			dump.RemoveFromIndexIP4(ip4.IP4, pack.ID)
		}
	}
}

func (pack *PackedContent) InsertIP4(ip4 IP4) {
	for _, existedIP4 := range pack.IP4 {
		if ip4 == existedIP4 {
			return
		}
	}

	pack.IP4 = append(pack.IP4, ip4)
}

func (pack *PackedContent) RemoveIP4(ip4 IP4) {
	for i, existedIP4 := range pack.IP4 {
		if ip4 == existedIP4 {
			pack.IP4 = append(pack.IP4[:i], pack.IP4[i+1:]...)

			return
		}
	}
}

func (dump *Dump) ExtractAndApplyIP6(record *Content, pack *PackedContent) {
	if len(record.IP6) > 0 {
		pack.IP6 = record.IP6
		for _, ip4 := range pack.IP6 {
			dump.InsertToIndexIP6(string(ip4.IP6), pack.ID)
		}
	}
}

func (dump *Dump) EctractAndApplyUpdateIP6(record *Content, pack *PackedContent) {
	ipExisted := make(map[string]Nothing, len(pack.IP6))
	if len(record.IP6) > 0 {
		for _, ip6 := range record.IP6 {
			pack.InsertIP6(ip6)

			addr := string(ip6.IP6)
			dump.InsertToIndexIP6(addr, pack.ID)
			ipExisted[addr] = Nothing{}
		}
	}

	for _, ip6 := range pack.IP6 {
		if _, ok := ipExisted[string(ip6.IP6)]; !ok {
			pack.RemoveIP6(ip6)
			dump.RemoveFromIndexIP6(string(ip6.IP6), pack.ID)
		}
	}
}

func (pack *PackedContent) InsertIP6(ip6 IP6) {
	for _, existedIP6 := range pack.IP6 {
		if string(ip6.IP6) == string(existedIP6.IP6) && ip6.Ts == existedIP6.Ts {
			return
		}
	}

	pack.IP6 = append(pack.IP6, ip6)
}

func (pack *PackedContent) RemoveIP6(ip6 IP6) {
	for i, existedIP6 := range pack.IP6 {
		if string(ip6.IP6) == string(existedIP6.IP6) && ip6.Ts == existedIP6.Ts {
			pack.IP6 = append(pack.IP6[:i], pack.IP6[i+1:]...)

			return
		}
	}
}

func (dump *Dump) ExtractAndApplySubnet4(record *Content, pack *PackedContent) {
	if len(record.Subnet4) > 0 {
		pack.Subnet4 = record.Subnet4
		for _, subnet4 := range pack.Subnet4 {
			dump.InsertToIndexSubnet4(subnet4.Subnet4, pack.ID)
		}
	}
}

func (dump *Dump) EctractAndApplyUpdateSubnet4(record *Content, pack *PackedContent) {
	subnetExisted := NewStringSet(len(pack.Subnet4))
	if len(record.Subnet4) > 0 {
		for _, subnet4 := range record.Subnet4 {
			pack.InsertSubnet4(subnet4)
			dump.InsertToIndexSubnet4(subnet4.Subnet4, pack.ID)
			subnetExisted[subnet4.Subnet4] = Nothing{}
		}
	}

	for _, subnet4 := range pack.Subnet4 {
		if _, ok := subnetExisted[subnet4.Subnet4]; !ok {
			pack.RemoveSubnet4(subnet4)
			dump.RemoveFromSubnet4(subnet4.Subnet4, pack.ID)
		}
	}
}

func (pack *PackedContent) InsertSubnet4(subnet4 Subnet4) {
	for _, existedSubnet4 := range pack.Subnet4 {
		if subnet4 == existedSubnet4 {
			return
		}
	}

	pack.Subnet4 = append(pack.Subnet4, subnet4)
}

func (pack *PackedContent) RemoveSubnet4(subnet4 Subnet4) {
	for i, existedSubnet4 := range pack.Subnet4 {
		if subnet4 == existedSubnet4 {
			pack.Subnet4 = append(pack.Subnet4[:i], pack.Subnet4[i+1:]...)

			return
		}
	}
}

func (dump *Dump) ExtractAndApplySubnet6(record *Content, pack *PackedContent) {
	if len(record.Subnet6) > 0 {
		pack.Subnet6 = record.Subnet6
		for _, subnet6 := range pack.Subnet6 {
			dump.InsertToIndexSubnet4(subnet6.Subnet6, pack.ID)
		}
	}
}

func (dump *Dump) EctractAndApplyUpdateSubnet6(record *Content, pack *PackedContent) {
	subnetExisted := NewStringSet(len(pack.Subnet6))
	if len(record.Subnet6) > 0 {
		for _, subnet6 := range record.Subnet6 {
			pack.InsertSubnet6(subnet6)
			dump.InsertToIndexSubnet6(subnet6.Subnet6, pack.ID)
			subnetExisted[subnet6.Subnet6] = Nothing{}
		}
	}

	for _, subnet6 := range pack.Subnet6 {
		if _, ok := subnetExisted[subnet6.Subnet6]; !ok {
			pack.RemoveSubnet6(subnet6)
			dump.RemoveFromSubnet4(subnet6.Subnet6, pack.ID)
		}
	}
}

func (pack *PackedContent) InsertSubnet6(subnet6 Subnet6) {
	for _, existedSubnet6 := range pack.Subnet6 {
		if subnet6 == existedSubnet6 {
			return
		}
	}

	pack.Subnet6 = append(pack.Subnet6, subnet6)
}

func (pack *PackedContent) RemoveSubnet6(subnet6 Subnet6) {
	for i, existedSubnet6 := range pack.Subnet6 {
		if subnet6 == existedSubnet6 {
			pack.Subnet6 = append(pack.Subnet6[:i], pack.Subnet6[i+1:]...)

			return
		}
	}
}

func (dump *Dump) ExtractAndApplyDomain(record *Content, pack *PackedContent) {
	if len(record.Domain) > 0 {
		pack.Domain = record.Domain
		for _, domain := range pack.Domain {
			nDomain := NormalizeDomain(domain.Domain)

			dump.InsertToIndexDomain(nDomain, pack.ID)
		}
	}
}

func (dump *Dump) EctractAndApplyUpdateDomain(record *Content, pack *PackedContent) {
	domainExisted := NewStringSet(len(pack.Domain))
	if len(record.Domain) > 0 {
		for _, domain := range record.Domain {
			pack.InsertDomain(domain)

			nDomain := NormalizeDomain(domain.Domain)

			dump.InsertToIndexDomain(nDomain, pack.ID)

			domainExisted[domain.Domain] = Nothing{}
		}
	}

	for _, domain := range pack.Domain {
		if _, ok := domainExisted[domain.Domain]; !ok {
			pack.RemoveDomain(domain)

			nDomain := NormalizeDomain(domain.Domain)

			dump.RemoveFromIndexDomain(nDomain, pack.ID)
		}
	}
}

func (pack *PackedContent) InsertDomain(domain Domain) {
	for _, existedDomain := range pack.Domain {
		if domain == existedDomain {
			return
		}
	}

	pack.Domain = append(pack.Domain, domain)
}

func (pack *PackedContent) RemoveDomain(domain Domain) {
	for i, existedDomain := range pack.Domain {
		if domain == existedDomain {
			pack.Domain = append(pack.Domain[:i], pack.Domain[i+1:]...)

			return
		}
	}
}

func (dump *Dump) ExtractAndApplyURL(record *Content, pack *PackedContent) {
	if len(record.URL) > 0 {
		pack.URL = record.URL
		for _, u := range pack.URL {
			nURL := NormalizeURL(u.URL)
			if strings.HasPrefix(nURL, "https://") {
				record.HTTPSBlock++
			}

			dump.InsertToIndexURL(nURL, pack.ID)
		}
	}

	pack.BlockType = record.constructBlockType()
}

func (dump *Dump) EctractAndApplyUpdateURL(record *Content, pack *PackedContent) {
	urlExisted := NewStringSet(len(pack.URL))
	HTTPSBlock := 0

	if len(record.URL) > 0 {
		for _, u := range record.URL {
			pack.InsertURL(u)

			nURL := NormalizeURL(u.URL)
			if strings.HasPrefix(nURL, "https://") {
				HTTPSBlock++
			}

			dump.InsertToIndexURL(nURL, pack.ID)

			urlExisted[u.URL] = Nothing{}
		}
	}

	record.HTTPSBlock = HTTPSBlock
	pack.BlockType = record.constructBlockType()

	for _, u := range pack.URL {
		if _, ok := urlExisted[u.URL]; !ok {
			pack.RemoveURL(u)

			nURL := NormalizeURL(u.URL)

			dump.RemoveFromIndexURL(nURL, pack.ID)
		}
	}
}

func (pack *PackedContent) InsertURL(u URL) {
	for _, existedURL := range pack.URL {
		if u == existedURL {
			return
		}
	}

	pack.URL = append(pack.URL, u)
}

func (pack *PackedContent) RemoveURL(u URL) {
	for i, existedURL := range pack.URL {
		if u == existedURL {
			pack.URL = append(pack.URL[:i], pack.URL[i+1:]...)

			return
		}
	}
}

func (pack *PackedContent) refreshPackedContent(hash uint64, utime int64, payload []byte) {
	pack.RecordHash, pack.RegistryUpdateTime, pack.Payload = hash, utime, payload
}

func newPackedContent(id int32, hash uint64, utime int64, payload []byte) *PackedContent {
	return &PackedContent{
		ID:                 id,
		RecordHash:         hash,
		RegistryUpdateTime: utime,
		Payload:            payload,
	}
}

func (v *PackedContent) newPbContent(ip4 uint32, ip6 []byte, domain, url, aggr string) *pb.Content {
	v0 := pb.Content{}
	v0.BlockType = v.BlockType
	v0.RegistryUpdateTime = v.RegistryUpdateTime
	v0.Id = v.ID
	v0.Ip4 = ip4
	v0.Ip6 = ip6
	v0.Domain = domain
	v0.Url = url
	v0.Aggr = aggr
	v0.Pack = v.Payload
	return &v0
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
				logger.Debug.Printf("Can't fetch id: %s: %s\n", _a.Value, err.Error())
			}
		}
	}
	return int32(id)
}

func parseRegister(element xml.StartElement, r *Reg) {
	for _, attr := range element.Attr {
		switch attr.Name.Local {
		case "formatVersion":
			r.FormatVersion = attr.Value
		case "updateTime":
			r.UpdateTime = parseRFC3339Time(attr.Value)
		case "updateTimeUrgently":
			r.UpdateTimeUrgently = attr.Value
		}
	}
}
