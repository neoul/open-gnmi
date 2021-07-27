# open-gnmi

open-source gnmi (gRPC Network Management Interface)

## Path Aliases

`open-gnmi` provides the Path Aliases for data paths. 
If an alias is defined as a schema path or with wildcards (*, ...), multiple data instances can be selected for the single alias.
The data returned in a SubscribeResponse message would become ambiguous because we cannot specify where the data comes from.

## Path Prefix

`open-gnmi` provides the Path Prefix that can be specified to Get, Set and Subscribe RPCs. In the case that a prefix is specified, the absolute path is comprised of the concatenation of the list of path elements representing the prefix and the list of path elements in the path field.

## TODO

- The parent node shold be removed if the parent node is created at update or replace RPC.
- Path Target: Only set to the prefix

## Sample gNMI Target

```bash
cd target
go build
./target -b :57400 --alsologtostderr --sample-interval 5s -v 100
```
