#!/bin/sh
set -eu

# Route the isolated app network through the gateway, then remove the ability
# to change routes or regain network capabilities from every descendant.
iface=$(ip -4 route show default | awk '{print $5; exit}')
ip route replace table 1000 default via 172.30.254.2 dev "$iface" onlink
ip -6 route replace table 1000 default via fd30:254::2 dev "$iface" onlink
for address in $(getent ahostsv4 postgres | awk '{print $1}' | sort -u); do
  ip route replace table 1000 "$address/32" dev "$iface"
done
for address in $(getent hosts postgres | awk '/:/ {print $1}' | sort -u); do
  ip -6 route replace table 1000 "$address/128" dev "$iface"
done
for family in -4 -6; do
  ip "$family" rule add pref 1000 fwmark 0x8790 lookup main
  ip "$family" rule add pref 1001 lookup 1000
done
nft -f - <<'EOF'
table inet artex_app {
  chain dns {
    type nat hook output priority -100;
    ip daddr 127.0.0.11 udp dport 53 dnat ip to 172.30.255.2
    ip daddr 127.0.0.11 tcp dport 53 dnat ip to 172.30.255.2
  }
  chain dns_source {
    type nat hook postrouting priority 100;
    ip saddr 127.0.0.0/8 ip daddr 172.30.255.2 masquerade
  }
  chain server_reply {
    type route hook output priority -150;
    tcp sport 8787 ct state established meta mark set 0x8790
  }
  chain backend_only {
    type filter hook output priority 0;
    meta mark 0x8792 accept
    ip daddr 172.30.254.2 tcp dport 8790 reject with tcp reset
    ip6 daddr fd30:254::2 tcp dport 8790 reject with tcp reset
    ip daddr { 127.0.0.0/8, 172.30.254.4 } tcp dport 8787 reject with tcp reset
    ip6 daddr { ::1, fd30:254::4 } tcp dport 8787 reject with tcp reset
    ip daddr 172.30.254.3 tcp dport 5432 reject with tcp reset
    ip6 daddr fd30:254::3 tcp dport 5432 reject with tcp reset
  }
}
EOF
printf 'nameserver 172.30.255.2\noptions timeout:2 attempts:2\n' > /etc/resolv.conf
# The Go backend drops these from every child's capability sets at startup.
exec setpriv --no-new-privs --bounding-set=-all,+net_raw,+setpcap --inh-caps=-all,+net_raw,+setpcap --ambient-caps=-all,+net_raw,+setpcap /app/start.sh "$@"
