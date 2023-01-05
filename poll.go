package main

import (
	"os"
	"runtime"
	"time"

	"google.golang.org/grpc"

	"github.com/usher2/u2ckdump/internal/logger"
)

func DumpPoll(s *grpc.Server, done chan bool, sigs chan os.Signal, url, token, dir string, d time.Duration) {
	runtime.GC()
	logger.Info.Printf("Complete GC\n")
	DumpRefresh(url, token, dir)
	for {
		timer := time.NewTimer(d * time.Second)
		select {
		case <-timer.C:
			DumpRefresh(url, token, dir)
		case <-sigs:
			s.Stop()
			done <- true
		}
	}
}

func DumpRefresh(url, token, dir string) {
	ts := time.Now().Unix()
	lastDump, err := GetLastDumpID(ts, url, token)
	if err != nil {
		logger.Error.Printf("Can't get last dump id: %s\n", err.Error())
		return
	}
	if lastDump.ID == "" {
		logger.Error.Println("Last dump Id is empty...")
		return
	}
	logger.Info.Printf("Last dump id: %s\n", lastDump.ID)
	cachedDump, err := ReadCurrentDumpID(dir + "/current")
	if err != nil {
		logger.Error.Printf("Can't read cached dump id: %s\n", err.Error())
		return
	}
	if cachedDump.ID == "" {
		logger.Warning.Println("Cashed dump Id is empty...")
	}
	// two states...
	if lastDump.CRC != cachedDump.CRC {
		logger.Info.Printf("Getting new dump..")
		err := FetchDump(lastDump.ID, dir+"/dump.zip", url, token)
		if err != nil {
			logger.Error.Printf("Can't fetch last dump: %s\n", err.Error())
			return
		}
		logger.Info.Println("Last dump fetched")
		err = DumpUnzip(dir+"/dump.zip", dir+"/dump.xml")
		if err != nil {
			logger.Error.Printf("Can't extract last dump: %s\n", err.Error())
			return
		}
		logger.Info.Println("Last dump extracted")
		// parse xml
		if dumpFile, err := os.Open(dir + "/dump.xml"); err != nil {
			logger.Error.Printf("Can't open dump file: %s\n", err.Error())
			return
		} else {
			defer dumpFile.Close()
			err = Parse(dumpFile)
			if err != nil {
				logger.Error.Printf("Parse error: %s\n", err.Error())
				return
			} else {
				logger.Info.Printf("Dump parsed")
				dumpFile.Close()
				runtime.GC()
				logger.Info.Printf("Complete GC\n")
			}
			err = WriteCurrentDumpID(dir+"/current", lastDump)
			if err != nil {
				logger.Error.Printf("Can't write currentdump file: %s\n", err.Error())
				return
			}
			logger.Info.Println("Last dump metainfo saved")
		}
	} else if lastDump.ID != cachedDump.ID {
		logger.Info.Printf("Not changed, but new dump metainfo")
		Parse2(lastDump.UpdateTime)
		runtime.GC()
		logger.Info.Printf("Complete GC\n")
	} else {
		logger.Info.Printf("No new dump")
	}
}
