all: test

test: build
	# TODO: the paths to the binaries has to be detected, or received from env vars
	sudo ./minio-perf -node FTothsg9ZuJubAEzZByEgQQUmkWM637x93YH1QSJM242 -mc ~/Projects/mc/mc -tfuser ~/tmp/tfuser
build:
	go build

.PHONY: build test
