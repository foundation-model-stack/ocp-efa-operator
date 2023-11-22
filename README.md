# ocp-efa-operator

ocp-efa-operator enables GPUDirect RDMA in an OpenShift cluster over AWS Elastic Fabric Adapter (EFA) so that distributed machine learning jobs speed up with optimized high performance networking. It deploys a required kernel module ([efa](https://github.com/amzn/amzn-drivers/tree/master/kernel/linux/efa)) and device plugin to let user pods access the special device functionality without privilege via a custom resource `fms.io/efa`. This also automates deployment of the kernel module for [GDRCOPY](https://github.com/NVIDIA/gdrcopy) (`gdrdrv`) and exposes `fms.io/gdrdrv` for user pods to optimize accessing GPU memory from host CPU by leveraging local RDMA.

## Description

We expect that cluster admins manage the operator and two cluster-wide CRDs (`efadrivers` and `gdrdrvdrivers`) to enable EFA and/or GDRCOPY in an OpenShift cluster.
The operator automatically detect EFA devices and CUDA drivers in a cluster and deploys required kernel modules.

Example pod specs are available under `test/*.yaml`. User pods can mount `/dev/gdrdrv` or `/dev/infiniband/uverbs*` by adding `fms.io/gdrdrv:1` or `fms.io/efa:1` at `resource.limits`.

User pods need custom container images with special user libraries.
Example Dockerfiles are also available under `test/Dockerfile.*`.

## Getting Started
You’ll need a Kubernetes cluster to run against. You can use [KIND](https://sigs.k8s.io/kind) to get a local cluster for testing, or run against a remote cluster.
**Note:** Your controller will automatically use the current context in your kubeconfig file (i.e. whatever cluster `kubectl cluster-info` shows).

### Prerequisite

The operator depends on some operators. The below operators must be deployed in a target cluster.

+ [GPU operator](https://github.com/NVIDIA/gpu-operator)
+ [Kernel module management operator](https://github.com/kubernetes-sigs/kernel-module-management)
+ [Node feature discovery](https://github.com/openshift/node-feature-discovery)

ocp-efa-operator will look up node labels for versions of cuda driver and linux kernels that the two operators generate.
It also utilizes Node feature discovery for EFA device detection at each node.

Note: currently, ocp-efa-operator can block unisntalling/upgrading GPU operator since it deploys gdrdrv depnding on nvidia kernel modules.
Admins need to carefully cleanup ocp-efa-operator at first before uninstalling/upgrading GPU operators.


### Install with a public bundle image

Run bundle with a public image
```bash
$ oc new-project ocp-efa-operator
$ operator-sdk run bundle ghcr.io/ocp-efa-operator-bundle:v0.0.1 --namespace ocp-efa-operator
```

Deploy GdrdrvDriver
```bash
$ oc apply -f config/sample/efa_v1alpha1_gdrdrvdriver.yaml
```

Deploy EfaDriver
```bash
$ oc apply -f config/sample/efa_v1alpha1_efadriver.yaml
```

Wait until the cluster gets ready
```bash
$ oc get efadrivers
NAME   STATUS
ocp    Ready
$ oc get gdrdrvdrivers
NAME   STATUS
ocp    Ready
```

### Advanced: Build and install from source

Build and push the device-plugin image
```bash
$ make dp-push IMG=myrepo.io/ocp-efa-device-plugin:v0.0.1
```

Build and push the operator image
```bash
$ make operator-push IMG=myrepo.io/ocp-efa-operator:v0.0.1
```

Build and push the bundle image
```bash
$ make bundle-push IMG=myrepo.io/ocp-efa-operator-bundle:v0.0.1
```

Run bundle with the pushed image at your namespace and secret
```bash
$ oc new-project ocp-efa-operator
$ oc create secret generic mysecret -n ocp-efa-operator --from-file=.dockerconfigfile=my.docker.config --type=kubernetes.io/dockerconfigjson
$ operator-sdk run bundle myrepo.io/ocp-efa-operator-bundle:v0.0.1 --pull-secret-name mysecret --namespace ocp-efa-operator
```

Deploy GdrdrvDriver
```bash
$ oc apply -f config/sample/efa_v1alpha1_gdrdrvdriver.yaml
```

Deploy EfaDriver
```bash
$ oc apply -f config/sample/efa_v1alpha1_efadriver.yaml
```

Wait until the cluster gets ready
```bash
$ oc get efadrivers
NAME   STATUS
ocp    Ready
$ oc get gdrdrvdrivers
NAME   STATUS
ocp    Ready
```

### Testing

*GDRCOPY:*

Build a gdrcopy test image
```bash
$ docker build -f test/Dockerfile.gdrcopy -t myrepo.io/gdrcopy-test:ocp-efa-v0.0.1 ./test
```

Try testing a gdrcopy test job
```bash
$ vim test/gdrcopy-test.yaml # modify image and imagePullSecrets
$ oc apply -f test/gdrcopy-test.yaml
```

*EFA Pingpong and NCCL Tests AllReduce:*

Build a nccl test image
```bash
$ docker build -f test/Dockerfile.nccl-tests -t myrepo.io/nccl-tests:ocp-efa-v0.0.1 ./test
```

Try testing an efa pingpong
```bash
$ vim test/efa-test-pingpong.yaml # modify image and imagePullSecrets
$ oc apply -f test/gdrcopy-test.yaml
```

Try testing a nccl-tests on mpijob (requires [training operator](https://github.com/kubeflow/training-operator))
```bash
$ vim test/efa-test-pingpong.yaml # modify image and imagePullSecrets
$ oc apply -f test/gdrcopy-test.yaml
```

*Pytorch AllReduce:*

Build a pytorch test image
```bash
$ docker build -f test/Dockerfile.pytorch -t myrepo.io/pytorch:ocp-efa-v0.0.1 ./test
```

Try testing a pytorch (requires [training operator](https://github.com/kubeflow/training-operator))
```bash
$ vim test/efa-test-pingpong.yaml # modify image and imagePullSecrets
$ oc apply -f test/gdrcopy-test.yaml
```

### Cleanup

Delete CRs
```bash
$ oc delete -f config/sample/efa_v1alpha1_efadriver.yaml
$ oc delete -f config/sample/efa_v1alpha1_gdrdrvdriver.yaml
```

Cleanup bundle
```bash
$ operator-sdk cleanup ocp-efa-operator --delete-all
```
