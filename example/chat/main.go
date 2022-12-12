// Chat example
//
// This application is capable of exchanging small text payloads between two connections.
// Just run it, twice on the same machine, or one onto one machine and another to another one (choose different network topologies for experimenting).
// Once started, it will print the addresses of the application:
// * if you run both instances on the same machine just give the local address of instance 1 to the instance 2 and the connection should be established;
// * if you run these on different machines, use remote address in the same way.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/rs/zerolog"

	"github.com/yottta/hopconn"
)

func main() {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()
	conn, err := hopconn.New(ctx, hopconn.WithLoggingLevel(zerolog.WarnLevel))
	if err != nil {
		panic(err)
	}
	incomingData := make(chan string, 10)
	conn.RegisterDataHandler(func(dat []byte) {
		incomingData <- string(dat)
	})
	exit := make(chan os.Signal, 1)
	signal.Notify(exit, os.Interrupt, syscall.SIGTERM, syscall.SIGKILL)
	go func() {
		defer cancelFunc()
		select {
		case <-exit:
			fmt.Println("-> Exit called")
		case e := <-conn.Errors():
			fmt.Printf("-> Exit because error returned by connection: %s\n", e)
		}

	}()

	fmt.Printf("-> Current client addresses:\n-> \tLocal: %s\n-> \tRemote: %s\n", conn.LocalAddress(), conn.PublicAddress())

	stdinData := make(chan string, 2)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				reader := bufio.NewReader(os.Stdin)
				text, err := reader.ReadString('\n')
				if err != nil {
					panic(err)
				}
				stdinData <- strings.Replace(text, "\n", "", -1)
			}
		}
	}()
	fmt.Printf("-> Insert target addresses separated by comma: ")
	select {
	case nextLine := <-stdinData:
		addresses := strings.Split(nextLine, ",")
		fmt.Printf("-> Inserted addresses: %s\n", addresses)

		if err := conn.AttemptConnection(addresses...); err != nil {
			if !errors.Is(err, hopconn.ErrAlreadyEstablished) {
				panic(err)
			}
		}
	case <-conn.EstablishedEvents():
		fmt.Println()
		fmt.Println("-> Skipping other connections as the waiting connection was established")
	case err := <-conn.Errors():
		fmt.Println()
		fmt.Println("-> Connection stopped", err)
		return
	}
	fmt.Println("-> Connection established! Type your message and hit Enter")

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-incomingData:
				fmt.Println()
				fmt.Println("-> Received: ", m)
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-stdinData:
			if err := conn.Write([]byte(m)); err != nil {
				fmt.Println("-> Error sending data", err)
			}
		}
	}
}
