#!/bin/bash

tmux kill-session -t frost-signers
tmux kill-session -t operators
rm -rf _data
