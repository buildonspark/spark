# Minikube

[Minikube](https://minikube.sigs.k8s.io/) is a Kubernetes cluster on your local machine. It provides an isolated environment to run and interact with Kubernetes workloads just like you would in a real cluster.

## Instructions

1. Install Minikube:

   If you have an M4 Macbook you need to build qemu with a patch:

       $ brew install minikube socket_vmnet
       $ brew install --build-from-source --formula ./spark/minikube/qemu.rb
       $ brew pin qemu

   Otherwise install normally:

       $ brew install minikube qemu socket_vmnet
   For other operating systems [download and install Minikube](https://minikube.sigs.k8s.io/docs/start/).
2. (OS X only) Start `socket_vmnet`

       $ sudo brew services start socket_vmnet
       $ sudo chmod g+x /opt/homebrew/var/run
2. Checkout this `spark` repo
3. Run the setup script:

       $ minikube/setup.sh

   Normally you will be able to see this output:
   ```
   ➜  ~ ~/ws/spark/minikube/setup.sh
       minikube
       type: Control Plane
       host: Stopped
       kubelet: Stopped
       apiserver: Stopped
       kubeconfig: Stopped

       😄  minikube v1.32.0 on Darwin 14.1.1 (arm64)
       ✨  Using the qemu2 driver based on existing profile
       👍  Starting control plane node minikube in cluster minikube
       🔄  Restarting existing qemu2 VM for "minikube" ...
       🐳  Preparing Kubernetes v1.28.3 on Docker 24.0.7 ...
           ▪ apiserver.service-node-port-range=1024-65535
       🔗  Configuring bridge CNI (Container Networking Interface) ...
       🔎  Verifying Kubernetes components...
           ▪ Using image gcr.io/k8s-minikube/storage-provisioner:v5
       🌟  Enabled addons: default-storageclass, storage-provisioner
       🏄  Done! kubectl is now configured to use "minikube" cluster and "default" namespace by default
       ✅  minikube profile was successfully set to minikube
   ```

`kubectl` will be setup with a new "minikube" context.

## Troubleshooting

To query state inside a minikube node you can use `kubectl` or terminal UI tool [k9s](https://k9scli.io/).
