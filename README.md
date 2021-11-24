# tlscheck

https://github.com/ThomasHabets/tlscheck

This is not an official Google product.



## Building

```
$ go build tlscheck.go
```

## Example

```
$ cat > endpoints.txt

# Simple connect to load balancer.
www.bing.com:443

# Connect to a specific backend and use SNI for real host.
www.bing.com/a-0001.a-msedge.net:443
^D

$ ./tlscheck < endpoints.txt
[… any warnings or errors …]
```
