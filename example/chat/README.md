# P2P Chat

A simple example of connecting between two chat clients by exchanging theirs addresses manually.
In a real system you would have a central server that will help clients exchanging their public/private addresses

## Run

This example shows how to connect between two local clients.

For connecting two remote clients, use Remote address instead.

### Run first client (later referred as client1)
```shell
%> go run .
-> Current client addresses:
->      Local: 192.168.100.54:61178
->      Remote: 111.111.111.111:61179
-> Insert target addresses separated by comma: 
```

### Run second client (later referred as client2)
```shell
%> go run .
-> Current client addresses:
->      Local: 192.168.100.54:61180
->      Remote: 111.111.111.111:61181
-> Insert target addresses separated by comma:
```

### Connect 
In order to connect you need to get one of the local address of one of the clients and give it to the other one.
So I will just type the client2's `Local` address into the client1's prompt and hit Enter.
```shell
%> go run .
-> Current client addresses:
->      Local: 192.168.100.54:61178
->      Remote: 111.111.111.111:61179
-> Insert target addresses separated by comma: 192.168.100.54:61180
-> Inserted addresses: [192.168.100.54:61180]
-> Connection established! Type your message and hit Enter
```

In the same time in the prompt for client2 you will see the following:
```shell
%> go run .
-> Current client addresses:
->      Local: 192.168.100.54:61180
->      Remote: 111.111.111.111:61181
-> Insert target addresses separated by comma:
-> Skipping other connections as the waiting connection was established
-> Connection established! Type your message and hit Enter
```

### Exchange
Now you can type anything and hit Enter on any of the clients and the other should receive it.

_client1 sending_:
```
%> go run .
-> Current client addresses:
->      Local: 192.168.100.54:61178
->      Remote: 111.111.111.111:61179
-> Insert target addresses separated by comma: 192.168.100.54:61180
-> Inserted addresses: [192.168.100.54:61180]
-> Connection established! Type your message and hit Enter
hi from conn1 (hit enter)
```

_client2 receiving_:
```
%> go run .
-> Current client addresses:
->      Local: 192.168.100.54:61180
->      Remote: 111.111.111.111:61181
-> Insert target addresses separated by comma:
-> Skipping other connections as the waiting connection was established
-> Connection established! Type your message and hit Enter
-> Received:  hi from conn1
```