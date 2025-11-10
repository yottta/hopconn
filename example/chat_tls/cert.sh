#!/bin/bash

if [ $# -ne 2 ]; then
  echo "invalid call. Eg: ./$(basename "$0") 35.172.214.8 82.137.47.95"
  exit 1
fi

# 1. Generate CA private key
openssl genrsa -out root_ca.key 2048

# 2. Generate CA certificate signed by the key generated at #1
openssl req -x509 -new -nodes -key root_ca.key -sha256 -days 1024 -out root_ca.pem  -subj "/C=RO/ST=Brasov/L=Brasov/O=andpec/OU=andpec/CN=server.aws/"

# 3. Generate client private key
openssl genrsa -out server.key 2048

# 4. Generate client certificate request (CSR) having the common name the IP that the connection is meant to connect to
openssl req -new -key server.key -out server.csr -subj "/C=RO/ST=Brasov/L=Brasov/O=andpec/OU=hopconn/CN=server.aws/"

# 5. Generate client certificate having the subjectAltName pointing to the IP that this connection is meant to connect to
openssl x509 -req -extfile <(printf "subjectAltName=IP:%s" "$1") -in server.csr -CA root_ca.pem -CAkey root_ca.key -CAcreateserial -out server.crt -days 3650 -sha256


# 3. Generate client private key
openssl genrsa -out client.key 2048

# 4. Generate client certificate request (CSR) having the common name the IP that the connection is meant to connect to
openssl req -new -key client.key -out client.csr -subj "/C=RO/ST=Brasov/L=Brasov/O=andpec/OU=hopconn/CN=server.aws/"

# 5. Generate client certificate having the subjectAltName pointing to the IP that this connection is meant to connect to
openssl x509 -req -extfile <(printf "subjectAltName=IP:%s" "$2") -in client.csr -CA root_ca.pem -CAkey root_ca.key -CAcreateserial -out client.crt -days 3650 -sha256

rm client.csr server.csr                                                                                                                                                                                                                                                 /0.0s
