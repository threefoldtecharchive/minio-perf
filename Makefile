all: test

test: build
	sudo ./minio-perf -node FTothsg9ZuJubAEzZByEgQQUmkWM637x93YH1QSJM242 -tfuser ~/tmp/tfuser
build:
	go build

.PHONY: build test
