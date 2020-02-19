# Running the tests
## Building
```bash
make
```

## Running the test
- the minio-perf will deploy the zdb namespaces randomly on some nodes on the grid
- minio-perf requires `tfuser` and also `mc` to execute the tests. By default minio-perf will find them using $PATH but if they are in other locations, you can set where by providing the `-tfuser` and `-mc` flags.
- **NOTE** you need to run minio-perf with sudo because it will configure wireguard automatically for the tests

```bash
sudo ./minio-perf -node $(NODE) -mc $(MC) -tfuser $(TFUSER)
```

## Usage
```bash
# ./minio-perf --help
flag provided but not defined: -hel
Usage of ./minio-perf:
  -dist string
    	distribution of data/party bit in format of Data/Parity (default "2/1")
  -mc string
    	path to mc binary. Default to using $PATH
  -node string
    	node to install minio. It must have public interface (optional)
  -tfuser string
    	path to tfuser binary. Default to using $PATH
  -zdbs int
    	number of zdb namespaces to deploy (default 3)
```
