#!/usr/bin/env bash
# Manufactures a 3-hop peel chain on Sepolia LINK so the fraud rule has something to flag.
#   A (DEPLOYER_PRIVATE_KEY) -> B -> C -> D, each hop forwards 95% of what it received.
# Prints the hop tx hashes; the first one is the one to feed `cre workflow simulate`.
#
# Needs in A: ~0.03 Sepolia ETH and >= 1 LINK. Burner wallets B, C, D are generated here and
# their keys written to scripts/.peel-wallets (gitignored). Testnet only.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a; source .env; set +a
CAST=${CAST:-$HOME/.foundry/bin/cast}
RPC=$SEPOLIA_RPC_URL
LINK=$TOKEN_ADDRESS
A_KEY=$DEPLOYER_PRIVATE_KEY
A=$($CAST wallet address --private-key "$A_KEY")
AMOUNT=${AMOUNT:-1000000000000000000}   # 1 LINK, 18 decimals
GAS_ETH=${GAS_ETH:-0.004}                # ETH sent to each hop for its one transfer

WALLETS=scripts/.peel-wallets
if [ ! -f "$WALLETS" ]; then
  for n in B C D; do
    out=$($CAST wallet new --json)
    addr=$(echo "$out" | python3 -c 'import json,sys;print(json.load(sys.stdin)["data"][0]["address"])')
    key=$(echo "$out" | python3 -c 'import json,sys;print(json.load(sys.stdin)["data"][0]["private_key"])')
    echo "$n $addr $key" >> "$WALLETS"
  done
  chmod 600 "$WALLETS"
fi
B_ADDR=$(awk '$1=="B"{print $2}' $WALLETS); B_KEY=$(awk '$1=="B"{print $3}' $WALLETS)
C_ADDR=$(awk '$1=="C"{print $2}' $WALLETS); C_KEY=$(awk '$1=="C"{print $3}' $WALLETS)
D_ADDR=$(awk '$1=="D"{print $2}' $WALLETS)

echo "A=$A B=$B_ADDR C=$C_ADDR D=$D_ADDR"
echo "A balances: $($CAST balance $A --rpc-url $RPC --ether) ETH, $($CAST call $LINK 'balanceOf(address)(uint256)' $A --rpc-url $RPC) LINK-wei"

send_eth() { $CAST send --private-key "$1" --rpc-url $RPC --value "${2}ether" "$3" --json | python3 -c 'import json,sys;d=json.load(sys.stdin);print((d.get("data") or d)["transactionHash"])'; }
send_link() { $CAST send --private-key "$1" --rpc-url $RPC $LINK 'transfer(address,uint256)' "$2" "$3" --json | python3 -c 'import json,sys;d=json.load(sys.stdin);print((d.get("data") or d)["transactionHash"])'; }
pct95() { python3 -c "print($1*95//100)"; }

echo "gas: A -> B, C"
send_eth "$A_KEY" $GAS_ETH $B_ADDR >/dev/null
send_eth "$A_KEY" $GAS_ETH $C_ADDR >/dev/null

H1=$(send_link "$A_KEY" $B_ADDR $AMOUNT);            echo "hop1 A->B $AMOUNT  $H1"
V2=$(pct95 $AMOUNT); H2=$(send_link "$B_KEY" $C_ADDR $V2); echo "hop2 B->C $V2  $H2"
V3=$(pct95 $V2);     H3=$(send_link "$C_KEY" $D_ADDR $V3); echo "hop3 C->D $V3  $H3"

echo
echo "trigger tx for simulate: $H1   (event index: position of the LINK Transfer log in that tx, usually 0)"
echo "subject that should score 30+ for peel_chain: $A"
