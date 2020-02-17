# Running the tests
## Building
```bash
make
```

## Running the test
- the minio-perf will deploy the zdb namespaces randomly on some nodes on the grid
- `minio-perf` requires only a node where minio is gonna be deployed, that is because we need to create, a network on that node as well. so it has to have a public interface.
- we still could do that also randomly, but may be in a later release.
- minio-perf requires `tfuser` and also `mc` to execute the tests. By default minio-perf will find them using $PATH but if they are in other locations, you can set where by providing the `-tfuser` and `-mc` flags.
- **NOTE** you need to run minio-perf with sudo because it will configure wireguard automatically for the tests

```bash
sudo ./minio-perf -node $(NODE) -mc $(MC) -tfuser $(TFUSER)
```
