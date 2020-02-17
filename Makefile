all: build

test: build
	sudo ./minio-perf -node $(NODE) -mc $(MC) -tfuser $(TFUSER)
build:
	go build

.PHONY: build test
