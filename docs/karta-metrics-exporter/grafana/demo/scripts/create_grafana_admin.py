# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
"""Create the Grafana administrator Secret once without printing or saving its password."""
import argparse
import json
import secrets
import subprocess

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--context', required=True)
args = parser.parse_args()
secret = {'apiVersion':'v1','kind':'Secret',
          'metadata':{'name':'karta-monitoring-admin','namespace':'monitoring'},
          'type':'Opaque','stringData':{'username':'admin','password':secrets.token_urlsafe(48)}}
# create, rather than apply, leaves an existing administrator credential untouched.
subprocess.run(['kubectl','--context',args.context,'create','-f','-'],
               input=json.dumps(secret).encode(),check=True)
