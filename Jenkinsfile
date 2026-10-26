// CI/CD for go-crud-api on the pipeline-lab Jenkins (see devops_pipeline/pipeline_lab).
//
// Flow: GitHub PR -> Jenkins -> go test -> golangci-lint -> Semgrep -> SBOM ->
//       docker build -> Trivy -> push registry -> helm lint/template ->
//       update GitOps repo -> Argo CD sync -> smoke test
//
// Tool stages (go test/lint/semgrep/syft/trivy) run as ephemeral pods on the 'kubernetes'
// cloud and move files via stash/unstash (Jenkins remoting). Do NOT run them via
// `docker run -v $PWD:...` on the controller: DOCKER_HOST points at the jenkins-docker DinD
// sidecar, whose container filesystem is NOT the controller's workspace, so any bind mount
// silently scans/tests an empty directory. docker build/save/push themselves are fine via
// DOCKER_HOST (build context is streamed, save/load are API responses, not bind mounts).
// Push/Helm/GitOps/Argo CD/smoke-test stages run on the controller ('built-in'), which has
// docker, kubectl, helm, argocd, and git baked in (see jenkins/Dockerfile).

def toolPod(String containerName, String image, Map<String, String> env = [:]) {
  String envYaml = env ? "\n    env:\n" + env.collect { k, v -> "    - name: ${k}\n      value: \"${v}\"" }.join("\n") : ''
  return """
apiVersion: v1
kind: Pod
spec:
  containers:
  - name: jnlp
    image: jenkins/inbound-agent:latest-jdk21
    workingDir: /home/jenkins/agent
  - name: ${containerName}
    image: ${image}
    command: ['cat']
    tty: true
    workingDir: /home/jenkins/agent${envYaml}
"""
}

pipeline {
  agent none

  options {
    timeout(time: 45, unit: 'MINUTES')
    disableConcurrentBuilds()
    buildDiscarder(logRotator(numToKeepStr: '20'))
    ansiColor('xterm')
  }

  parameters {
    string(name: 'REGISTRY', defaultValue: 'registry:5000', description: 'Container registry host[:port]')
    string(name: 'IMAGE_NAME', defaultValue: 'go-crud-api', description: 'Image repository name (without registry)')
    string(name: 'REGISTRY_CREDENTIALS_ID', defaultValue: '', description: 'usernamePassword credential ID for registry login; leave empty for an unauthenticated registry')
    string(name: 'HELM_CHART_PATH', defaultValue: 'charts/go-crud-api', description: 'Path to the Helm chart')
    string(name: 'GITOPS_REPO', defaultValue: 'git@github.com:example/curd-app-gitops.git', description: 'GitOps repository SSH URL')
    string(name: 'GITOPS_SSH_CREDENTIALS_ID', defaultValue: 'gitops-ssh-key', description: 'sshUserPrivateKey credential ID for the GitOps repo')
    string(name: 'GITOPS_VALUES_PATH', defaultValue: 'values-staging.yaml', description: 'Values file (relative to the GitOps repo root) to update')
    string(name: 'ARGOCD_APP_NAME', defaultValue: 'go-crud-api-staging', description: 'Argo CD Application name to sync')
    string(name: 'ARGOCD_CREDENTIALS_ID', defaultValue: 'argocd-token', description: 'Secret text credential ID holding the Argo CD auth token')
    string(name: 'K8S_NAMESPACE', defaultValue: 'staging', description: 'Namespace the service is deployed into')
    string(name: 'SERVICE_NAME', defaultValue: 'go-crud-api', description: 'Helm release / k8s Service name for the smoke test')
  }

  environment {
    IMAGE = "${params.REGISTRY}/${params.IMAGE_NAME}"
  }

  stages {
    stage('Checkout') {
      agent { label 'built-in' }
      steps {
        checkout scm
        script { env.IMAGE_TAG = sh(script: 'git rev-parse --short=12 HEAD', returnStdout: true).trim() }
        stash name: 'src', includes: '**'
      }
    }

    stage('Go Test') {
      // golang:*-alpine has no C toolchain (gcc/musl-dev) -> `-race` fails with
      // "requires cgo; enable cgo by setting CGO_ENABLED=1". Use the Debian-based
      // image, which ships gcc, instead.
      agent { kubernetes { yaml toolPod('golang', 'golang:1.26') } }
      steps {
        container('golang') {
          unstash 'src'
          sh 'go vet ./...'
          sh 'go test -race -cover ./...'
        }
      }
    }

    stage('Lint (golangci-lint)') {
      agent { kubernetes { yaml toolPod('golangci-lint', 'golangci/golangci-lint:latest') } }
      steps {
        container('golangci-lint') {
          unstash 'src'
          sh 'golangci-lint run --timeout 5m ./...'
        }
      }
    }

    stage('SAST (Semgrep)') {
      // semgrep/semgrep auto-detects "running in Docker" and expects the conventional
      // `-v $PWD:/src` bind mount, crashing on an unrelated empty /src otherwise.
      // SEMGREP_IN_DOCKER=0 disables that heuristic; scan target is passed explicitly too.
      agent { kubernetes { yaml toolPod('semgrep', 'semgrep/semgrep:latest', [SEMGREP_IN_DOCKER: '0']) } }
      steps {
        container('semgrep') {
          unstash 'src'
          sh 'semgrep scan --config auto --error --json -o semgrep-results.json .'
        }
      }
      post {
        always {
          container('semgrep') { archiveArtifacts artifacts: 'semgrep-results.json', allowEmptyArchive: true }
        }
      }
    }

    stage('SBOM (Syft)') {
      // anchore/syft's default image is FROM scratch (binary only, no shell/cat) ->
      // ContainerCannotRun: "cat": executable file not found. The "debug" tag adds busybox.
      agent { kubernetes { yaml toolPod('syft', 'anchore/syft:debug') } }
      steps {
        container('syft') {
          unstash 'src'
          sh 'syft dir:. -o cyclonedx-json=sbom.cdx.json'
          archiveArtifacts artifacts: 'sbom.cdx.json', fingerprint: true
        }
      }
    }

    stage('Docker Build') {
      agent { label 'built-in' }
      steps {
        unstash 'src'
        sh "docker build -t ${IMAGE}:${env.IMAGE_TAG} ."
        sh "docker save -o image.tar ${IMAGE}:${env.IMAGE_TAG}"
        stash name: 'image-tar', includes: 'image.tar'
      }
    }

    stage('Trivy Scan') {
      agent { kubernetes { yaml toolPod('trivy', 'aquasec/trivy:latest') } }
      steps {
        container('trivy') {
          unstash 'image-tar'
          sh 'trivy image --input image.tar --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 --no-progress'
        }
      }
    }

    stage('Push Registry') {
      agent { label 'built-in' }
      steps {
        script {
          if (params.REGISTRY_CREDENTIALS_ID?.trim()) {
            withCredentials([usernamePassword(credentialsId: params.REGISTRY_CREDENTIALS_ID, usernameVariable: 'REG_USER', passwordVariable: 'REG_PASS')]) {
              sh "echo \"\$REG_PASS\" | docker login ${params.REGISTRY} -u \"\$REG_USER\" --password-stdin"
            }
          }
        }
        sh "docker tag ${IMAGE}:${env.IMAGE_TAG} ${IMAGE}:latest"
        sh "docker push ${IMAGE}:${env.IMAGE_TAG}"
        sh "docker push ${IMAGE}:latest"
      }
    }

    stage('Helm Lint/Template') {
      agent { label 'built-in' }
      steps {
        unstash 'src'
        sh "helm lint ${params.HELM_CHART_PATH}"
        sh """helm template ${params.SERVICE_NAME} ${params.HELM_CHART_PATH} \
          --set image.repository=${IMAGE} \
          --set image.tag=${env.IMAGE_TAG} > rendered.yaml"""
        archiveArtifacts artifacts: 'rendered.yaml', fingerprint: true
      }
    }

    stage('Update GitOps Repo') {
      when { branch 'main' }
      agent { label 'built-in' }
      steps {
        withCredentials([sshUserPrivateKey(credentialsId: params.GITOPS_SSH_CREDENTIALS_ID, keyFileVariable: 'GITOPS_KEY')]) {
          sh """#!/usr/bin/env bash
            set -euo pipefail
            export GIT_SSH_COMMAND="ssh -i \$GITOPS_KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"
            rm -rf gitops-workspace
            git clone --depth 1 ${params.GITOPS_REPO} gitops-workspace
            cd gitops-workspace
            sed -i -E "s#^(\\s*repository:).*#\\1 ${IMAGE}#; s#^(\\s*tag:).*#\\1 \\"${env.IMAGE_TAG}\\"#" ${params.GITOPS_VALUES_PATH}
            git config user.name 'jenkins-bot'
            git config user.email 'jenkins-bot@example.com'
            git add ${params.GITOPS_VALUES_PATH}
            git commit -m "chore(release): deploy ${params.IMAGE_NAME} ${env.IMAGE_TAG} [skip ci]" --allow-empty
            git push origin HEAD:main
          """
        }
      }
    }

    stage('Argo CD Sync') {
      when { branch 'main' }
      agent { label 'built-in' }
      steps {
        withCredentials([string(credentialsId: params.ARGOCD_CREDENTIALS_ID, variable: 'ARGOCD_AUTH_TOKEN')]) {
          timeout(time: 10, unit: 'MINUTES') {
            sh """argocd app sync ${params.ARGOCD_APP_NAME} --server \$ARGOCD_SERVER --insecure --grpc-web
              argocd app wait ${params.ARGOCD_APP_NAME} --server \$ARGOCD_SERVER --insecure --grpc-web --sync --health --timeout 540"""
          }
        }
      }
    }

    stage('Smoke Test') {
      when { branch 'main' }
      agent { label 'built-in' }
      steps {
        sh """#!/usr/bin/env bash
          set -euo pipefail
          kubectl -n ${params.K8S_NAMESPACE} run smoke-test-${env.BUILD_NUMBER} --rm -i --restart=Never \
            --image=curlimages/curl --command -- \
            sh -c 'curl -sf http://${params.SERVICE_NAME}.${params.K8S_NAMESPACE}.svc.cluster.local:8080/healthz \
              && curl -sf http://${params.SERVICE_NAME}.${params.K8S_NAMESPACE}.svc.cluster.local:8080/readyz'
        """
      }
    }
  }

  post {
    always {
      node('built-in') {
        sh 'rm -f image.tar || true'
        cleanWs()
      }
    }
  }
}
