#!/bin/bash

tmux kill-session -t bitcoind
tmux kill-session -t operators
tmux kill-session -t frost-signers
tmux kill-session -t electrs
