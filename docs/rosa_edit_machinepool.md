## rosa edit machinepool

Edit machine pool

### Synopsis

Edit the additional machine pool from a cluster.

```
rosa edit machinepool [flags]
```

### Examples

```
  # Set 4 replicas on machine pool 'mp1' on cluster 'mycluster'
  rosa edit machinepool --replicas=4 --cluster=mycluster mp1
  # Enable autoscaling and Set 3-5 replicas on machine pool 'mp1' on cluster 'mycluster'
  rosa edit machinepool --enable-autoscaling --min-replicas=3 max-replicas=5 --cluster=mycluster mp1
```

### Options

```
  -c, --cluster string       Name or ID of the cluster to add the machine pool to (required).
      --enable-autoscaling   Enable autoscaling for the machine pool.
  -h, --help                 help for machinepool
      --max-replicas int     Maximum number of machines for the machine pool.
      --min-replicas int     Minimum number of machines for the machine pool.
      --replicas int         Count of machines for this machine pool.
```

### Options inherited from parent commands

```
      --debug            Enable debug mode.
  -i, --interactive      Enable interactive mode.
      --profile string   Use a specific AWS profile from your credential file.
  -v, --v Level          log level for V logs
```

### SEE ALSO

* [rosa edit](rosa_edit.md)	 - Edit a specific resource

