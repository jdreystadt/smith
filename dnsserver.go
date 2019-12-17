// dnsserver is a small dns server capable of authoritative responses
// for local machines, going out to a remote server for non-local
// names, and caching non-local responses to minimize the load
package main

import (
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Constants for status
const (
	stNewMesg = iota
	stCheckingLocal
	stNotLocal
	stCheckingCache
	stNotCache
	stCheckingRemote
	stFoundAnswer
	stSentAnswer
	stExpired
)

// iqSessKey has the remote address and the remote session id.
type iqSessKey struct {
	iqAddr   string
	iqMesgID uint16
}

type iqSessData struct {
	state    int
	status   int
	timer    time.Time
	oqMesgID uint16
	qMessage dnsmessage.Message
}

type iqMesg struct {
	iqKey  iqSessKey
	iqData iqSessData
}

type oqSessKey struct {
	oqMesgID uint16
}

type oqSessData struct {
	iqAddr   string
	iqMesgID uint16
}

type oqMesg struct {
	oqKey  oqSessKey
	oqData oqSessData
}

type aTime struct {
	iqTime time.Time
	iqKey  iqSessKey
}

func dnsServer(statusChan chan<- string, commandChan <-chan string, rDNS, lDNS string) {
	// This is a dns proxy server. It answers what questions it can,
	// and sends out to a remote server for anything it cannot answer.
	// Open port for incoming messages
	// Open port for outgoing messages and replies
	// Create map for ongoing operations, channel for timeouts
	// Start listener for incoming messages
	// Start listener for replies
	// Start talker for outgoing messages
	// Start timer for timeouts
	// In a loop, run the next two loops
	//   In a loop, listen for incoming messages, replies, timeouts, commands
	//   In a loop, process our state
	// End of main loop

	var lConn, rConn net.PacketConn
	var err error
	var closing = false

	lConn, err = net.ListenPacket("udp", lDNS)
	if err != nil {
		fmt.Println("dnsServer - can't open local port")
		statusChan <- "Shutdown"
		return
	}
	defer lConn.Close()

	var rAddr *net.UDPAddr
	rAddr, err = net.ResolveUDPAddr("udp", rDNS)
	if err != nil {
		fmt.Println("dnsServer - can't resolve remote port")
		statusChan <- "Shutdown"
		return
	}
	rConn, err = net.DialUDP("udp", nil, rAddr)
	if err != nil {
		fmt.Println("dnsServer - can't open remote port")
		statusChan <- "Shutdown"
		return
	}
	defer rConn.Close()

	iqMesgChan := make(chan iqMesg, 1)
	orMesgChan := make(chan iqMesg, 1)
	clMesgChan := make(chan iqMesg, 1)
	clRespChan := make(chan iqMesg, 1)
	ccMesgChan := make(chan iqMesg, 1)

	oqMesgChan := make(chan iqMesg, 1)

	lTimerReqChan := make(chan aTime, 1)
	lTimerRespChan := make(chan aTime, 1)

	rTimerReqChan := make(chan aTime, 1)
	rTimerRespChan := make(chan aTime, 1)

	closeChan := make(chan interface{}, 0)

	iqMap := make(map[iqSessKey]iqSessData, 100)

	fmt.Println("dnsServer - starting servers")

	// Two routines for getting a query and
	// sending the response
	go listenIncoming(closeChan, iqMesgChan, lConn)
	go sendResponse(orMesgChan, lConn)
	// Three routines for the different ways to
	// determine the answer to a query
	go checkLocal(clMesgChan, clRespChan)
	go checkCache(ccMesgChan, clRespChan)
	go checkRemote(oqMesgChan, clRespChan, rConn)
	// A pair of timers, one for checking remote
	// and one for discarding answered queries
	go timer(closeChan, rTimerReqChan, rTimerRespChan, 5)
	go timer(closeChan, lTimerReqChan, lTimerRespChan, 30)

	for {
		var command string
		var newMesg iqMesg
		var curSession iqSessData
		var newTimerEvent aTime
		var ok bool

		// Wait for an event
		select {
		case command, _ = <-commandChan:
			switch command {
			case "Close":
				closing = true
				close(closeChan)
				fmt.Println("dnsServer - Close received")
			case "Cancel":
				closing = true
				lConn.Close()
				fmt.Println("dnsServer - Cancel received")
				statusChan <- "Shutdown"
				return
			default:
				fmt.Println("dnsServer - unknown command received - ", command)
			}
		case newMesg = <-iqMesgChan:
			fmt.Println("got a message")
			curSession, ok = iqMap[newMesg.iqKey]
			if !ok {
				// Pass to checkLocal
				newMesg.iqData.state = stCheckingLocal
				clMesgChan <- newMesg
				// Start a thirty second timer to expire the session
				newTimerEvent.iqTime = time.Now()
				newMesg.iqData.timer = newTimerEvent.iqTime
				newTimerEvent.iqKey = newMesg.iqKey
				lTimerReqChan <- newTimerEvent
				// Create the new session for followup
				iqMap[newMesg.iqKey] = newMesg.iqData
			} else {
				// Got a resend for an existing session
				if curSession.state == stSentAnswer {
					// Looks like we sent an answer but it was not received so resend
					newMesg.iqData = curSession
					orMesgChan <- newMesg
				}
			}
		case newMesg = <-clRespChan:
			fmt.Println("got a message from clRespChan")
			// See if present in the map
			curSession, ok = iqMap[newMesg.iqKey]
			if !ok {
				fmt.Println("dnsServer - got bad message on clRespChan", newMesg.iqKey)
				continue
			}
			if newMesg.iqData.qMessage.Response {
				// Found an answer so update the session and send out an answer
				newMesg.iqData.state = stSentAnswer
				orMesgChan <- newMesg
				iqMap[newMesg.iqKey] = newMesg.iqData
				continue
			}
			// Response was still false so try again
			if curSession.state == stCheckingLocal {
				// Try checking cache
				newMesg.iqData.state = stCheckingCache
				clMesgChan <- newMesg
				iqMap[newMesg.iqKey] = newMesg.iqData
				continue
			}
			if curSession.state == stCheckingCache {
				newMesg.iqData.state = stCheckingRemote
				oqMesgChan <- newMesg
				iqMap[newMesg.iqKey] = newMesg.iqData
				continue
			}
			if curSession.state == stCheckingRemote {
				// Should be impossible
				fmt.Println("dnsServer - checkRemote failed")
			}
		case newTimerEvent, ok = <-lTimerRespChan:
			if !ok {
				fmt.Println("dnsServer - lTimerRespChan closed")
				return
			}
			// Are we waiting on this?
			if curSession.timer != newTimerEvent.iqTime {
				continue
			}
			delete(iqMap, newTimerEvent.iqKey)
			fmt.Println("Deleted a session, count is", len(iqMap))
			break
		case newTimerEvent, ok = <-rTimerRespChan:
			if !ok {
				fmt.Println("dnsServer - rTimerRespChan closed")
				return
			}
			if newTimerEvent.iqKey.iqMesgID == 0 {
				continue
			}
		}
		if closing && len(iqMesgChan) == 0 {
			statusChan <- "Shutdown"
			return
		}
	}
}

func listenIncoming(closeChan chan interface{}, mesgChan chan iqMesg, conn net.PacketConn) {
	var err error
	var buffer []byte
	var addr net.Addr
	var n int

	buffer = make([]byte, 2048)

outerloop:
	for {
		n, addr, err = conn.ReadFrom(buffer)
		if err != nil {
			// Don't complain if conn is closed
			if !strings.HasSuffix(err.Error(), "use of closed network connection") {
				fmt.Println("listenIncoming - got error -", err.Error())
			}
			break
		}
		fmt.Println("listenIncoming - got message")
		// Check if we are closing down
		select {
		case _, ok := <-closeChan:
			if !ok {
				continue outerloop
			}
		default:
		}
		var newMesg dnsmessage.Message
		err = newMesg.Unpack(buffer[0:n])
		// ok, time to send the message for processing
		var sendMesg iqMesg
		sendMesg.iqKey.iqMesgID = newMesg.ID
		sendMesg.iqKey.iqAddr = addr.String()
		sendMesg.iqData.qMessage = newMesg
		mesgChan <- sendMesg
	}
}

func sendResponse(mesgChan chan iqMesg, conn net.PacketConn) {
	var err error
	var outMesg iqMesg
	var outMessage dnsmessage.Message
	var outBuffer []byte
	var outAddr *net.UDPAddr
	var ok bool
	for {
		select {
		case outMesg, ok = <-mesgChan:
			if !ok {
				fmt.Println("sendResponse - Message channel closed")
				return
			}
			// OK, we have something to do
			outMessage = outMesg.iqData.qMessage
			outMessage.RecursionAvailable = true
			outMessage.Response = true
			outMessage.Additionals = nil
			outBuffer, err = outMessage.Pack()
			if err != nil {
				fmt.Println("sendResponse - got an error packing", err)
			} else {
				outAddr, err = net.ResolveUDPAddr("udp", outMesg.iqKey.iqAddr)
				if err != nil {
					fmt.Println("sendResponse - got an error resolving", err)
					continue
				}
				_, err := conn.WriteTo(outBuffer, outAddr)
				if err != nil {
					fmt.Println("sendResponse - got an error writing", err)
				}
			}
		}

	}
}

func sendQuery(mesgChan chan iqMesg, conn net.PacketConn) {

}

func readQuery(mesgChan chan iqMesg, conn net.PacketConn) {

}

func timer(closeChan chan interface{}, aTimeChan chan aTime, aTimeEventChan chan aTime, delaySeconds int) {
	// Get a message, see if the time has expired, sleep if not, return if so.
	// Use closeChan to see if we are shutting down

	var timerDelay = time.Duration(delaySeconds) * time.Second
	var ok bool
	var closing = false
	var timer aTime
	var eventTime time.Duration

	for {
		select {
		case _, ok = <-closeChan:
			if !ok {
				closing = true
			}
			continue
		case timer, ok = <-aTimeChan:
			if !ok {
				if !closing {
					fmt.Println("timer - aTimeChan closed")
				}
				return
			}
		}
		eventTime = timer.iqTime.Add(timerDelay).Sub(time.Now())
		if eventTime > 0 {
			time.Sleep(eventTime)
		}
		aTimeEventChan <- timer
	}
}

func checkLocal(mesgChan chan iqMesg, respChan chan iqMesg) {
	// checkLocal uses the Response field in the message to signal if it has figured
	// out the answer.
	var ok bool
	var aMesg iqMesg
	for {
		aMesg, ok = <-mesgChan
		fmt.Println("checkLocal got a message")
		if !ok {
			fmt.Println("checkLocal - mesgChan closed")
			return
		}
		nameLen := aMesg.iqData.qMessage.Questions[0].Name.Length
		name := string(aMesg.iqData.qMessage.Questions[0].Name.Data[:nameLen])
		fmt.Println("checkLocal -", name)
		if strings.HasSuffix(name, ".localdomain.") {
			// Got a match
			fmt.Println("checkLocal matched")
			aMesg.iqData.qMessage.Header.Response = true
		}
		respChan <- aMesg
	}
}

func checkCache(mesgChan chan iqMesg, respChan chan iqMesg) {
	var aMesg iqMesg
	var ok bool
	for {
		select {
		case aMesg, ok = <-mesgChan:
			if !ok {
				fmt.Println("checkCache - mesgChan closed")
				return
			}
			respChan <- aMesg
		}
	}
}

func checkRemote(mesgChan chan iqMesg, respChan chan iqMesg, conn net.PacketConn) {
	// We have two goroutines, sendRemote and listenRemote, that talk to network.
	// We also have a timer running so we drop our sessions as needed. So three
	// incoming channels, mesgChan to request a lookup, lrChan to give the response,
	// and timerChan for the timer.
	// Create any needed channels and maps.
	// Start our goroutines.
	// Loop reading our inputs.
	// Exit if mesgChan is closed and our session count is zero.

	var closing = false
	var ok bool
	var aMesg iqMesg
	var mySession uint16
	var newTimerEvent aTime

	closeChan := make(chan interface{}, 0)
	timerChan := make(chan aTime, 1)
	timerRespChan := make(chan aTime, 1)
	srChan := make(chan iqMesg, 1)
	lrChan := make(chan iqMesg, 1)

	sessionMap := make(map[uint16]iqSessKey, 10)
	sessReverseMap := make(map[iqSessKey]uint16, 10)

	go timer(closeChan, timerChan, timerRespChan, 30)
	go sendQuery(srChan, conn)
	go readQuery(lrChan, conn)

	for {
		select {
		case aMesg, ok = <-mesgChan:
			if !ok {
				fmt.Println("checkRemote - mesgChan closed")
				closing = true
				continue
			}
			mySession++
			sessionMap[mySession] = aMesg.iqKey
			sessReverseMap[aMesg.iqKey] = mySession
			aMesg.iqData.qMessage.ID = mySession
			srChan <- aMesg
		case newTimerEvent = <-timerRespChan:
			aSession := sessReverseMap[newTimerEvent.iqKey]
			delete(sessionMap, aSession)
			delete(sessReverseMap, newTimerEvent.iqKey)
			if closing && len(sessionMap) == 0 {
				return
			}
		case aMesg = <-lrChan:
			aSession := sessReverseMap[aMesg.iqKey]
			aMesg.iqData.qMessage.Header.ID = aSession
			respChan <- aMesg
		}
	}
}
