package main

import (
	"log"
	"os"
)

func main() {
	f, _ := os.CreateTemp("", "zuse.log")
	log.SetOutput(f)
}
