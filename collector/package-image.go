//go:build ignore

// Package main creates release OCI assets without publishing a registry image.
package main

import (
	"flag"
	"fmt"
	"hostlier/internal/collectorimage"
	"os"
)

// main packages the two laptop-built executable architectures with a trusted CA bundle.
func main() {
	output := flag.String("output", "collector/dist/collector-image", "new OCI layout directory")
	ca := flag.String("ca", "", "PEM public certificate bundle from the pinned image")
	arm := flag.String("arm64", "collector/dist/hostlier-collector-linux-arm64", "Linux arm64 executable")
	amd := flag.String("amd64", "collector/dist/hostlier-collector-linux-amd64", "Linux amd64 executable")
	flag.Parse()
	// Certificate material is an explicit release input, never fetched from an arbitrary URL.
	if *ca == "" {
		fmt.Fprintln(os.Stderr, "--ca is required")
		os.Exit(2)
	}
	inputs := []collectorimage.PlatformInput{{Architecture: "arm64", BinaryPath: *arm, CAPath: *ca}, {Architecture: "amd64", BinaryPath: *amd, CAPath: *ca}}
	// Failed packaging leaves no partially published layout.
	if err := collectorimage.Build(*output, inputs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	image, err := collectorimage.Open(*output)
	// Validate the exact artifact the installer will later consume.
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer image.Close()
	fmt.Printf("collector-image %s platforms=%v\n", image.Digest(), image.Platforms())
}
