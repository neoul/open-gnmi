# open-gnmi

open-source gnmi (gRPC Network Management Interface)

This is renewal of gnxi.

## Aliases

open-gnmi only provides Path Aliases for data paths.
If an alias is defined as a schema path or with wildcards (*, ...), multiple data instances can be selected for the single alias.
The data returned in a SubscribeResponse message would become ambiguous because we cannot specify where the data comes from.

## TODO

- The parent node shold be removed if the parent node is created at update or replace RPC.


- Path Target: Only set to the prefix