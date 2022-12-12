# hopconn

A Golang library meant for establishing hole punch connections. 

Based on the [Peer-to-Peer Communication Across Network Address Translators](https://bford.info/pub/net/p2pnat/index.html).


## Description of hole punching

![](/Users/andrei.ciobanu/data/workspace/projects/99_experiments/hopconn/docs/architecture.png)

In the diagram you can see also client2 and client3 which are in the same network therefore the connection is done by one of the clients listening and the other one dialing.

In the following section we will shortly and briefly discuss the basic idea behind hole punching. For more details see the paper linked at the beginning of the README.md.
### Basic description
For the hole punching to work, both clients needs to know the following:
* Target client public IP.
* Target client public port.


_There are multiple ways to exchange this info between clients. 
The most known one is by using a proxy server accessible publicly by both clients where they register and ask for the counter peer information_ 

In simple words the connection is established as follows:
1) Client1 **dials** the client4's `publicIP:publicPort`.
2) Client2 **dials** the client1's `publicIP:publicPort`.
3) The NAT will see the other client's request incoming and considers it as a response to the initially performed request therefore will allow it.
4) Connection established.


> As the paper at the top is also saying and also from my own tests, this technique is not guaranteed to work across all NATs therefore you should retry first in order to be sure that this is working well.
> 
> One way to test if this technique will work in a specific network topology is to use `netcat`.
> 
> run on machine1: `nc -p 1234 machine2-public-ip 1233`  
> run on machine1: `nc -p 1233 machine1-public-ip 1234`  
> To get the public IP of the machines you can use `curl ifconfig.me` or any of the multiple alternatives online.

## Details of the library
### Ports allocation
In order to be able to communicate, the library is creating 2 net.Listener to get 2 free ports from the system.
One is used for private connectivity and the other one is used to allocate the public port that later will be used to Dial the peer in order to initiate the hole punching mechanism.

### Public IP
Also, by default, the library is using `https://api.ipify.org` to get its public IP that it is needed for the hole punching mechanism.
You can override this behaviour by passing a new function for public IP discovery using `WithPublicIPProvider` during calling `NewConn`.

### Read data
In order to read the data that the connection is receiving you should register at least one handler by using `RegisterDataHandler`.
If no handler is registered, the data will just be discarded.

### State control
To stop the connection, two mechanisms are available:
* `Close()` method exposed by the connection.
* Pass a cancellable context when the constructor is called.

## How is it meant to be used?
1) Call `NewConn`. During this phase, use `With` method for specific configurations of the connection.
2) Use `RegisterDataHandler()` for adding methods that will be called with the data read from the connection.
3) Use `LocalAddress()` and `PublicAddress()` in order to retrieve local and public addresses on which the connection can be established.
4) Establish connection
   1) Call `AttemptConnection()` with the address(es) of the peer.
   2) Call `AttemptConnection` from the other peer with the addresses from the step 2
5) Listen on `<-conn.EstablishedEvents()` in order to know when the connection was established.
6) To know when/if the connection was cancelled, listen on `<-conn.Errors()` that will signal connectivity issues. When an error is received on this channel the user of the library should start establishing a new connection. 
7) Use `Write()` to write data to the connection.
8) Use `Close()` or cancel the context that was given to the constructor during step #1.

### 
For some examples you can check the [dedicated directory](example).