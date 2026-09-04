# ft_lgtm

Web application letting the users run code in a safe environment.

`make` boots the guest with Vagrant and libvirt, then builds the backend and the frontend
images into the containerd of its K3s. It stops there and deploys nothing, so a fresh
station has no `lgtm` namespace yet.

The manifests and the dashboards live in
[ft_lgtm_gitops](https://github.com/AxelCharlot/ft_lgtm_gitops), and Argo CD applies them.

`make dev` rebuilds and restarts after a code change. `make hosts-client` prints the three
lines the browser's machine needs in its own hosts file.
