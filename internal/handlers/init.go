package handlers

import (
	"fmt"
	"os"
	"path/filepath"

	cli "github.com/urfave/cli/v2"
)

// CmdInit handles the init command
func CmdInit(c *cli.Context) error {
	provider := c.String("provider")
	template := c.String("template")
	output := c.String("output")
	force := c.Bool("force")

	// Determine output file
	if output == "" {
		switch provider {
		case "github":
			output = ".github/workflows/ci.yml"
		case "gitlab":
			output = ".gitlab-ci.yml"
		case "bitbucket":
			output = "bitbucket-pipelines.yml"
		case "azure":
			output = "azure-pipelines.yml"
		case "circleci":
			output = ".circleci/config.yml"
		case "drone":
			output = ".drone.yml"
		case "travis":
			output = ".travis.yml"
		default:
			output = ".github/workflows/ci.yml"
		}
	}

	// Check if file exists
	if _, err := os.Stat(output); err == nil && !force {
		return fmt.Errorf("file %s already exists. Use --force to overwrite", output)
	}

	// Create directory if needed
	dir := filepath.Dir(output)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Generate pipeline content
	content := generatePipelineTemplate(provider, template)

	// Write file
	if err := os.WriteFile(output, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write file %s: %w", output, err)
	}

	fmt.Printf("✓ Created %s pipeline: %s\n", provider, output)
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  1. Review and customize the pipeline\n")
	fmt.Printf("  2. Test locally: git-ci run -f %s\n", output)
	fmt.Printf("  3. Commit and push to repository\n")

	return nil
}

// generatePipelineTemplate generates a pipeline template
func generatePipelineTemplate(provider, template string) string {
	switch provider {
	case "github":
		return generateGitHubTemplate(template)
	case "gitlab":
		return generateGitLabTemplate(template)
	case "circleci":
		return generateCircleCITemplate(template)
	case "drone":
		return generateDroneTemplate(template)
	case "travis":
		return generateTravisTemplate(template)
	case "bitbucket":
		return generateBitbucketTemplate(template)
	case "azure":
		return generateAzureTemplate(template)
	default:
		return generateGitHubTemplate(template)
	}
}

// generateGitHubTemplate generates GitHub Actions template
func generateGitHubTemplate(template string) string {
	switch template {
	case "node":
		return githubNodeTemplate
	case "python":
		return githubPythonTemplate
	case "go":
		return githubGoTemplate
	case "docker":
		return githubDockerTemplate
	default:
		return githubBasicTemplate
	}
}

// generateGitLabTemplate generates GitLab CI template
func generateGitLabTemplate(template string) string {
	switch template {
	case "node":
		return gitlabNodeTemplate
	case "python":
		return gitlabPythonTemplate
	case "go":
		return gitlabGoTemplate
	case "docker":
		return gitlabDockerTemplate
	default:
		return gitlabBasicTemplate
	}
}

// generateCircleCITemplate generates CircleCI template
func generateCircleCITemplate(template string) string {
	switch template {
	case "node":
		return circleciNodeTemplate
	case "python":
		return circleciPythonTemplate
	case "go":
		return circleciGoTemplate
	case "docker":
		return circleciDockerTemplate
	default:
		return circleciBasicTemplate
	}
}

// generateDroneTemplate generates Drone CI template
func generateDroneTemplate(template string) string {
	switch template {
	case "node":
		return droneNodeTemplate
	case "python":
		return dronePythonTemplate
	case "go":
		return droneGoTemplate
	case "docker":
		return droneDockerTemplate
	default:
		return droneBasicTemplate
	}
}

// generateTravisTemplate generates Travis CI template
func generateTravisTemplate(template string) string {
	switch template {
	case "node":
		return travisNodeTemplate
	case "python":
		return travisPythonTemplate
	case "go":
		return travisGoTemplate
	case "docker":
		return travisDockerTemplate
	default:
		return travisBasicTemplate
	}
}

// generateBitbucketTemplate generates Bitbucket Pipelines template
func generateBitbucketTemplate(template string) string {
	// Implement Bitbucket templates
	return bitbucketBasicTemplate
}

// generateAzureTemplate generates Azure Pipelines template
func generateAzureTemplate(template string) string {
	// Implement Azure templates
	return azureBasicTemplate
}

// Template definitions

const githubBasicTemplate = `name: CI

on:
  push:
    branches: [ main, develop ]
  pull_request:
    branches: [ main ]

jobs:
  test:
    runs-on: ubuntu-latest

    steps:
    - uses: actions/checkout@v3

    - name: Run tests
      run: echo "Add your test commands here"

  build:
    runs-on: ubuntu-latest
    needs: test

    steps:
    - uses: actions/checkout@v3

    - name: Build
      run: echo "Add your build commands here"
`

const githubNodeTemplate = `name: Node.js CI

on:
  push:
    branches: [ main, develop ]
  pull_request:
    branches: [ main ]

jobs:
  test:
    runs-on: ubuntu-latest

    strategy:
      matrix:
        node-version: [16.x, 18.x, 20.x]

    steps:
    - uses: actions/checkout@v3

    - name: Use Node.js ${{ matrix.node-version }}
      uses: actions/setup-node@v3
      with:
        node-version: ${{ matrix.node-version }}
        cache: 'npm'

    - name: Install dependencies
      run: npm ci

    - name: Run tests
      run: npm test

    - name: Run linter
      run: npm run lint

  build:
    runs-on: ubuntu-latest
    needs: test

    steps:
    - uses: actions/checkout@v3

    - name: Use Node.js
      uses: actions/setup-node@v3
      with:
        node-version: 18.x
        cache: 'npm'

    - name: Install dependencies
      run: npm ci

    - name: Build
      run: npm run build

    - name: Upload artifacts
      uses: actions/upload-artifact@v3
      with:
        name: build
        path: dist/
`

const githubPythonTemplate = `name: Python CI

on:
  push:
    branches: [ main, develop ]
  pull_request:
    branches: [ main ]

jobs:
  test:
    runs-on: ubuntu-latest

    strategy:
      matrix:
        python-version: ["3.8", "3.9", "3.10", "3.11"]

    steps:
    - uses: actions/checkout@v3

    - name: Set up Python ${{ matrix.python-version }}
      uses: actions/setup-python@v4
      with:
        python-version: ${{ matrix.python-version }}

    - name: Install dependencies
      run: |
        python -m pip install --upgrade pip
        pip install -r requirements.txt
        pip install pytest flake8

    - name: Lint with flake8
      run: |
        flake8 . --count --select=E9,F63,F7,F82 --show-source --statistics
        flake8 . --count --exit-zero --max-complexity=10 --max-line-length=127 --statistics

    - name: Test with pytest
      run: pytest
`

const githubGoTemplate = `name: Go CI

on:
  push:
    branches: [ main, develop ]
  pull_request:
    branches: [ main ]

jobs:
  test:
    runs-on: ubuntu-latest

    steps:
    - uses: actions/checkout@v3

    - name: Set up Go
      uses: actions/setup-go@v4
      with:
        go-version: 1.21

    - name: Install dependencies
      run: go mod download

    - name: Run tests
      run: go test -v -race -coverprofile=coverage.out ./...

    - name: Run vet
      run: go vet ./...

    - name: Run golangci-lint
      uses: golangci/golangci-lint-action@v3
      with:
        version: latest

  build:
    runs-on: ubuntu-latest
    needs: test

    steps:
    - uses: actions/checkout@v3

    - name: Set up Go
      uses: actions/setup-go@v4
      with:
        go-version: 1.21

    - name: Build
      run: go build -v ./...
`

const githubDockerTemplate = `name: Docker CI

on:
  push:
    branches: [ main, develop ]
  pull_request:
    branches: [ main ]

env:
  REGISTRY: ghcr.io
  IMAGE_NAME: ${{ github.repository }}

jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write

    steps:
    - uses: actions/checkout@v3

    - name: Set up Docker Buildx
      uses: docker/setup-buildx-action@v2

    - name: Log in to Container Registry
      uses: docker/login-action@v2
      with:
        registry: ${{ env.REGISTRY }}
        username: ${{ github.actor }}
        password: ${{ secrets.GITHUB_TOKEN }}

    - name: Extract metadata
      id: meta
      uses: docker/metadata-action@v4
      with:
        images: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}

    - name: Build and push Docker image
      uses: docker/build-push-action@v4
      with:
        context: .
        push: true
        tags: ${{ steps.meta.outputs.tags }}
        labels: ${{ steps.meta.outputs.labels }}
        cache-from: type=gha
        cache-to: type=gha,mode=max
`

const gitlabBasicTemplate = `stages:
  - test
  - build
  - deploy

variables:
  CI: "true"

test:
  stage: test
  script:
    - echo "Running tests..."
    - echo "Add your test commands here"

build:
  stage: build
  script:
    - echo "Building application..."
    - echo "Add your build commands here"
  dependencies:
    - test

deploy:
  stage: deploy
  script:
    - echo "Deploying application..."
    - echo "Add your deployment commands here"
  only:
    - main
  when: manual
`

const gitlabNodeTemplate = `image: node:18

stages:
  - test
  - build
  - deploy

variables:
  CI: "true"

cache:
  paths:
    - node_modules/

before_script:
  - npm ci

test:
  stage: test
  script:
    - npm run test
    - npm run lint
  coverage: '/Lines\s+:\s+(\d+\.\d+)%/'

build:
  stage: build
  script:
    - npm run build
  artifacts:
    paths:
      - dist/
    expire_in: 1 week
  dependencies:
    - test
`

const gitlabPythonTemplate = `image: python:3.11

stages:
  - test
  - build
  - deploy

variables:
  PIP_CACHE_DIR: "$CI_PROJECT_DIR/.cache/pip"

cache:
  paths:
    - .cache/pip
    - venv/

before_script:
  - python -m venv venv
  - source venv/bin/activate
  - pip install -r requirements.txt

test:
  stage: test
  script:
    - pip install pytest flake8
    - flake8 .
    - pytest --cov=.
  coverage: '/TOTAL.*\s+(\d+)%/'

build:
  stage: build
  script:
    - python setup.py bdist_wheel
  artifacts:
    paths:
      - dist/
  dependencies:
    - test
`

const gitlabGoTemplate = `image: golang:1.21

stages:
  - test
  - build

variables:
  GO111MODULE: "on"

cache:
  paths:
    - .go/pkg/mod/

before_script:
  - mkdir -p .go
  - export GOPATH=$CI_PROJECT_DIR/.go
  - export PATH=$PATH:$GOPATH/bin

test:
  stage: test
  script:
    - go mod download
    - go test -v -race -coverprofile=coverage.out ./...
    - go vet ./...
    - go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
    - golangci-lint run
  coverage: '/coverage: \d+.\d+% of statements/'

build:
  stage: build
  script:
    - go build -v -o app ./cmd/...
  artifacts:
    paths:
      - app
  dependencies:
    - test
`

const gitlabDockerTemplate = `image: docker:latest

services:
  - docker:dind

stages:
  - build
  - push

variables:
  DOCKER_DRIVER: overlay2
  DOCKER_TLS_CERTDIR: "/certs"
  IMAGE_TAG: $CI_REGISTRY_IMAGE:$CI_COMMIT_SHORT_SHA

build:
  stage: build
  script:
    - docker build -t $IMAGE_TAG .
    - docker save $IMAGE_TAG > image.tar
  artifacts:
    paths:
      - image.tar
    expire_in: 1 hour

push:
  stage: push
  before_script:
    - docker login -u $CI_REGISTRY_USER -p $CI_REGISTRY_PASSWORD $CI_REGISTRY
  script:
    - docker load < image.tar
    - docker push $IMAGE_TAG
    - docker tag $IMAGE_TAG $CI_REGISTRY_IMAGE:latest
    - docker push $CI_REGISTRY_IMAGE:latest
  only:
    - main
`

const bitbucketBasicTemplate = `pipelines:
  default:
    - step:
        name: Test
        script:
          - echo "Running tests..."
    - step:
        name: Build
        script:
          - echo "Building application..."
`

const azureBasicTemplate = `trigger:
- main

pool:
  vmImage: ubuntu-latest

stages:
- stage: Test
  jobs:
  - job: Test
    steps:
    - script: echo "Running tests..."
      displayName: 'Run tests'

- stage: Build
  dependsOn: Test
  jobs:
  - job: Build
    steps:
    - script: echo "Building application..."
      displayName: 'Build application'
`

// =============================================================================
// CircleCI Templates
// =============================================================================

const circleciBasicTemplate = `version: 2.1

orbs:
  go: circleci/go@3.2.1

executors:
  default:
    docker:
      - image: cimg/base:stable

jobs:
  test:
    executor: default
    steps:
      - checkout
      - run:
          name: Run tests
          command: echo "Add your test commands here"

  build:
    executor: default
    steps:
      - checkout
      - run:
          name: Build
          command: echo "Add your build commands here"

workflows:
  version: 2
  ci:
    jobs:
      - test
      - build:
          requires:
            - test
`

const circleciGoTemplate = `version: 2.1

orbs:
  go: circleci/go@3.2.1

executors:
  go-default:
    docker:
      - image: cimg/go:1.22

jobs:
  test:
    executor: go-default
    steps:
      - checkout
      - go/mod-download
      - go/test:
          race: true
          coverprofile: coverage.out

  lint:
    executor: go-default
    steps:
      - checkout
      - go/mod-download
      - run:
          name: Lint
          command: |
            go fmt ./...
            go vet ./...
            golangci-lint run --timeout 5m

  build:
    executor: go-default
    steps:
      - checkout
      - go/mod-download
      - run:
          name: Build
          command: go build -ldflags="-s -w" -o app .
      - persist_to_workspace:
          root: .
          paths:
            - app

workflows:
  version: 2
  ci:
    jobs:
      - lint
      - test:
          requires:
            - lint
      - build:
          requires:
            - test
`

const circleciNodeTemplate = `version: 2.1

orbs:
  node: circleci/node@5.0.0

executors:
  node-executor:
    docker:
      - image: cimg/node:20

jobs:
  test:
    executor: node-executor
    steps:
      - checkout
      - node/install-packages:
          pkg-manager: npm
      - run:
          name: Run tests
          command: npm test
      - run:
          name: Lint
          command: npm run lint

  build:
    executor: node-executor
    steps:
      - checkout
      - node/install-packages:
          pkg-manager: npm
      - run:
          name: Build
          command: npm run build
      - persist_to_workspace:
          root: .
          paths:
            - dist/

workflows:
  version: 2
  ci:
    jobs:
      - test
      - build:
          requires:
            - test
`

const circleciPythonTemplate = `version: 2.1

executors:
  python-executor:
    docker:
      - image: cimg/python:3.11

jobs:
  test:
    executor: python-executor
    parameters:
      python-version:
        type: string
        default: "3.11"
    steps:
      - checkout
      - run:
          name: Install dependencies
          command: |
            python -m pip install --upgrade pip
            pip install -r requirements.txt
            pip install pytest flake8
      - run:
          name: Lint
          command: flake8 . --count --select=E9,F63,F7,F82 --show-source --statistics
      - run:
          name: Test
          command: pytest

  build:
    executor: python-executor
    steps:
      - checkout
      - run:
          name: Build
          command: python setup.py sdist bdist_wheel

workflows:
  version: 2
  ci:
    jobs:
      - test
      - build:
          requires:
            - test
`

const circleciDockerTemplate = `version: 2.1

executors:
  docker-executor:
    docker:
      - image: cimg/base:stable

jobs:
  build:
    executor: docker-executor
    steps:
      - checkout
      - setup_remote_docker
      - run:
          name: Build Docker image
          command: docker build -t myapp:latest .

  push:
    executor: docker-executor
    steps:
      - checkout
      - setup_remote_docker
      - run:
          name: Log in to registry
          command: echo "$REGISTRY_PASSWORD" | docker login ghcr.io -u $REGISTRY_USER --password-stdin
      - run:
          name: Push Docker image
          command: |
            docker build -t ghcr.io/myorg/myapp:latest .
            docker push ghcr.io/myorg/myapp:latest

workflows:
  version: 2
  docker:
    jobs:
      - build
      - push:
          requires:
            - build
          filters:
            branches:
              only: main
`

// =============================================================================
// Drone CI Templates
// =============================================================================

const droneBasicTemplate = `kind: pipeline
type: docker
name: default

trigger:
  branch:
    - main
  event:
    - push
    - pull_request

steps:
  - name: test
    image: alpine:latest
    commands:
      - echo "Running tests..."

  - name: build
    image: alpine:latest
    commands:
      - echo "Building application..."
    depends_on:
      - test
`

const droneGoTemplate = `kind: pipeline
type: docker
name: go-ci

trigger:
  branch:
    - main
    - develop
  event:
    - push
    - pull_request

environment:
  GO_VERSION: "1.22"
  CGO_ENABLED: "0"

steps:
  - name: lint
    image: golang:${GO_VERSION}
    commands:
      - go fmt ./...
      - go vet ./...

  - name: test
    image: golang:${GO_VERSION}
    commands:
      - go mod download
      - go test -v -race -count=1 ./...
      - go test -race -coverprofile=coverage.out ./...

  - name: build
    image: golang:${GO_VERSION}
    commands:
      - go build -ldflags="-s -w" -o app .
    depends_on:
      - test
`

const droneNodeTemplate = `kind: pipeline
type: docker
name: node-ci

trigger:
  branch:
    - main
  event:
    - push
    - pull_request

environment:
  NODE_VERSION: "20"

steps:
  - name: install
    image: node:${NODE_VERSION}
    commands:
      - npm ci

  - name: test
    image: node:${NODE_VERSION}
    commands:
      - npm test
      - npm run lint
    depends_on:
      - install

  - name: build
    image: node:${NODE_VERSION}
    commands:
      - npm run build
    depends_on:
      - test
`

const dronePythonTemplate = `kind: pipeline
type: docker
name: python-ci

trigger:
  branch:
    - main
  event:
    - push
    - pull_request

environment:
  PYTHON_VERSION: "3.11"

steps:
  - name: test
    image: python:${PYTHON_VERSION}
    commands:
      - pip install -r requirements.txt
      - pip install pytest flake8
      - flake8 . --count --select=E9,F63,F7,F82 --show-source --statistics
      - pytest

  - name: build
    image: python:${PYTHON_VERSION}
    commands:
      - python setup.py sdist bdist_wheel
    depends_on:
      - test
`

const droneDockerTemplate = `kind: pipeline
type: docker
name: docker-ci

trigger:
  branch:
    - main
  event:
    - push

steps:
  - name: build
    image: plugins/docker
    settings:
      repo: myorg/myapp
      tags: latest
      dockerfile: Dockerfile

  - name: push
    image: plugins/docker
    settings:
      repo: myorg/myapp
      tags:
        - latest
        - "${DRONE_COMMIT_SHA:0:8}"
      dockerfile: Dockerfile
    depends_on:
      - build
    when:
      branch:
        - main
`

// =============================================================================
// Travis CI Templates
// =============================================================================

const travisBasicTemplate = `language: generic

os: linux
dist: focal

script:
  - echo "Running tests..."
  - echo "Add your test commands here"

jobs:
  include:
    - stage: test
      script:
        - echo "Running tests..."

    - stage: build
      script:
        - echo "Building application..."
`

const travisGoTemplate = `language: go

go:
  - "1.22"
  - "1.23"

env:
  global:
    - GO111MODULE=on
    - CGO_ENABLED=0

cache:
  directories:
    - $HOME/.cache/go-build
    - $HOME/go/pkg/mod

install:
  - go mod download

script:
  - go test -v -race -count=1 ./...
  - go vet ./...

after_success:
  - go build -ldflags="-s -w" -o app .

jobs:
  include:
    - stage: lint
      go: "1.22"
      script:
        - go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
        - golangci-lint run --timeout 5m
`

const travisNodeTemplate = `language: node_js

node_js:
  - "16"
  - "18"
  - "20"

cache:
  npm: true

install:
  - npm ci

script:
  - npm test
  - npm run lint

jobs:
  include:
    - stage: build
      node_js: "20"
      script:
        - npm run build
`

const travisPythonTemplate = `language: python

python:
  - "3.9"
  - "3.10"
  - "3.11"

cache:
  pip: true

install:
  - pip install -r requirements.txt
  - pip install pytest flake8

script:
  - flake8 . --count --select=E9,F63,F7,F82 --show-source --statistics
  - pytest

jobs:
  include:
    - stage: build
      python: "3.11"
      script:
        - python setup.py sdist bdist_wheel
`

const travisDockerTemplate = `language: minimal

os: linux
dist: focal

services:
  - docker

env:
  global:
    - REGISTRY=ghcr.io
    - IMAGE_NAME=myorg/myapp

script:
  - docker build -t $REGISTRY/$IMAGE_NAME:latest .

before_deploy:
  - echo "$REGISTRY_PASSWORD" | docker login $REGISTRY -u "$REGISTRY_USER" --password-stdin

deploy:
  provider: script
  script: docker push $REGISTRY/$IMAGE_NAME:latest
  on:
    branch: main
`
