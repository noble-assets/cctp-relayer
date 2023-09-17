# CCTP Relayer

<p align="center"><img src=".github/assets/portal.png"></p>

This service listens and forwards Cross Chain Transfer Protocol events.   
It is lightweight and extensible; other source/destination chains can be easily added.

Installation
```shell
git clone https://github.com/strangelove-ventures/noble-cctp-relayer
cd noble-cctp-relayer
go install
```

Running the relayer
```shell
rly start --config ./config/sample-app-config.yaml
```

# Generating ABI Go bindings

```shell
abigen --abi cmd/ethereum/abi/TokenMessenger.json --pkg cmd --type TokenMessenger --out cmd/TokenMessenger.go
abigen --abi cmd/ethereum/abi/ERC20.json --pkg integration_testing --type ERC20 --out integration/ERC20.go
```


State
| IrisLookupId | Type    | Status   | SourceDomain | DestDomain | SourceTxHash  | DestTxHash | MsgSentBytes | Created | Updated |
|:-------------|:--------|:---------|:-------------|:-----------|:--------------|:-----------|:-------------|:--------|:--------|
| 0x123        | Mint    | Created  | 0            | 4          | 0x123         | ABC123     | bytes...     | date    | date    |
| 0x123        | Forward | Pending  | 0            | 4          | 0x123         | ABC123     | bytes...     | date    | date    |
| 0x123        | Mint    | Attested | 0            | 4          | 0x123         | ABC123     | bytes...     | date    | date    |
| 0x123        | Forward | Complete | 0            | 4          | 0x123         | ABC123     | bytes...     | date    | date    |
| 0x123        | Mint    | Failed   | 0            | 4          | 0x123         | ABC123     | bytes...     | date    | date    |
| 0x123        | Mint    | Filtered | 0            | 4          | 0x123         | ABC123     | bytes...     | date    | date    |

