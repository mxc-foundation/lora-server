---
title: Source
menu:
    main:
        parent: community
        weight: 3
description: How to get the LoRa Server source and how to compile this into an executable binary.
---

# LoRa Server source

Source-code can be found at [https://github.com/mxc-foundation/lpwan-server](https://github.com/mxc-foundation/lpwan-server).

## Building

### With Docker

The easiest way to get started is by using the provided 
[docker-compose](https://docs.docker.com/compose/) environment. To start a bash
shell within the docker-compose environment, execute the following command from
the root of this project:

{{<highlight bash>}}
docker-compose run --rm loraserver bash
{{< /highlight >}}

### Without Docker

It is possible to build LoRa Server without Docker. However this requires
to install a couple of dependencies (depending your platform, there might be
pre-compiled packages available):

#### Go

Make sure you have [Go](https://golang.org/) installed (1.11+) as LoRa Server
uses Go modules, the repository must be cloned outside `$GOPATH`.

#### Go protocol buffer support

Install the C++ implementation of protocol buffers and Go support by following
the Go support for Protocol Buffers [installation instructions](https://github.com/golang/protobuf).

### Example commands

A few example commands that you can run:

{{<highlight bash>}}
# install development requirements
make dev-requirements

# run the tests
make test

# compile
make build

# compile snapshot builds for supported architectures
make snapshot
{{< /highlight >}}
