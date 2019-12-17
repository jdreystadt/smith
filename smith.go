// This is a demo go program designed to show how the UDP network protocol
// works with go. It is not intended as production quality code, just a
// learning experience.

// Put any +build lines immediately below
package main

import (
	"fmt"
	"os"
	"os/signal"
)

func main() {
	// Code overview
	// Constants
	// Setup signal handling
	// Create channels and initialize counter
	// Start servers
	// Loop on servers running
	// Select on signal channel and status channel
	// Signal = 1, send out a close message
	// Signal = 2, send out a cancel message
	// Signal = 3, break out
	// Status = shutdown, decrement counter
	// Status = display, send to web server
	// End of loop

	// Constants
	const remoteDNS = "8.8.8.8:53" // Use Google
	const localDNS = ":8053"
	const localWebserver = ":8080"

	// Set up signal handling
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)

	// Create channels and initialize counters
	statusChan := make(chan string, 1)
	serverCount := 0
	signalCount := 0

	// Start servers
	dnsCommandChan := make(chan string, 1)
	go dnsServer(statusChan, dnsCommandChan, remoteDNS, localDNS)
	serverCount++

	webCommandChan := make(chan string, 1)
	go webServer(statusChan, webCommandChan)
	serverCount++

forLoop:
	for serverCount > 0 {
		var statusMsg string
		var ok bool
		select {
		case statusMsg, ok = <-statusChan:
			if !ok {
				// Status channel is closed, abort
				break forLoop
			}
			switch statusMsg {
			case "Shutdown":
				fmt.Println("Shutdown received")
				serverCount--
			case "Display":
				fmt.Println("Message")
			}
		case <-signalChan:
			signalCount++
			switch signalCount {
			case 1:
				dnsCommandChan <- "Close"
				webCommandChan <- "Close"
			case 2:
				dnsCommandChan <- "Cancel"
				webCommandChan <- "Cancel"
			case 3:
				break forLoop
			}
		}
	}

}

func webServer(statusChan chan<- string, commandChan <-chan string) {
	for {
		var command string
		var ok bool
		command, ok = <-commandChan
		if !ok {
			fmt.Println("webServer - command channel is closed")
			break
		}
		switch command {
		case "Close":
			fmt.Println("webServer - Close received")
		case "Cancel":
			fmt.Println("webServer - Cancel received")
			statusChan <- "Shutdown"
		default:
			fmt.Println("webServer - unknown command received - ", command)
		}
	}
}
