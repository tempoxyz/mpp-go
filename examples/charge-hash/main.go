package main

import (
	"context"
	"fmt"
	"log"
)

func main() {
	ctx := context.Background()
	api, err := startServer()
	if err != nil {
		log.Fatal(err)
	}
	defer api.Close()

	result, err := runClient(ctx, api.url, api.rpc)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("broadcast hash credential and received receipt %s\n", result.Receipt)
}
