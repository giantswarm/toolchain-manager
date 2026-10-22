"""ATS smoke for the toolchain-manager chart.

app-test-suite (ATS >= 1.0) installs the packaged chart on the job's kind
cluster with `helm upgrade --install --wait` (the namespace from
.ats/main.yaml) and runs this file with `pytest -m smoke`: the generated
execute-chart-tests job of every branch build, whose dev image the branch
build pushed (branchPublish). The smoke is the chart-level install check: the
manager's Deployment ready on its defaults and the ToolchainSource CRD
established. Behaviour against the real platform (muster, Dex, the GitHub
App) is verified in agentlab. The dependencies are the generated
tests/ats/pyproject.toml, owned by giantswarm/devctl; this file and
.ats/main.yaml are the repository's own.
"""

import pykube
import pytest
from pytest_helm_charts.clusters import Cluster
from pytest_helm_charts.k8s.deployment import wait_for_deployments_to_run

NAMESPACE = "default"


@pytest.mark.smoke
def test_api_working(kube_cluster: Cluster) -> None:
    """The kind cluster ATS runs against is reachable."""
    assert kube_cluster.kube_client is not None
    assert len(pykube.Node.objects(kube_cluster.kube_client)) >= 1


@pytest.mark.smoke
def test_manager_runs(kube_cluster: Cluster) -> None:
    """The manager's Deployment becomes ready."""
    wait_for_deployments_to_run(kube_cluster.kube_client, ["toolchain-manager"], NAMESPACE, 120)


@pytest.mark.smoke
def test_crd_established(kube_cluster: Cluster) -> None:
    """The ToolchainSource CRD is established."""
    crd = pykube.CustomResourceDefinition.objects(kube_cluster.kube_client).get(name="toolchainsources.toolchain-manager.giantswarm.io")
    conditions = {c["type"]: c["status"] for c in crd.obj["status"]["conditions"]}
    assert conditions.get("Established") == "True"
