# open-gnmi

open-source gnmi (gRPC Network Management Interface)

This is renewal of gnxi.

## Aliases

open-gnmi provides Path Aliases for data paths.
If an alias is defined as a schema path or with wildcards (*, ...), multiple data instances are selected for the single alias. The data path returned in a SubscribeResponse message would become ambiguous.

## TODO

- The parent node shold be removed if the parent node is created at update or replace RPC.
