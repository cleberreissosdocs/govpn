package main

import (
	"net"
	"strconv"
)

func netSplitHostPort(addr string) (string, string, error) {
	return net.SplitHostPort(addr)
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
