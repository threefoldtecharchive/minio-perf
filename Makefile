all: build

test: build
	sudo ./minio-perf -mc $(MC) -tfuser $(TFUSER)
build:
	go build

.PHONY: build test
