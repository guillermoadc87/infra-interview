terraform {
  required_version = ">= 1.11.0"

  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.23"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.12"
    }
    tunnel = {
      source  = "dfns/tunnel"
      version = "~> 1.5"
    }
    postgresql = {
      source  = "cyrilgdn/postgresql"
      version = "~> 1.21"
    }
    vault = {
      source  = "hashicorp/vault"
      version = "~> 4.2"
    }
  }
}

provider "kubernetes" {
  config_path = "~/.kube/config"
}

provider "helm" {
  kubernetes {
    config_path = "~/.kube/config"
  }
}

ephemeral "tunnel_kubernetes" "postgres" {
  service_name = "postgres"
  namespace    = "interview-test"
  target_port  = 5432
  kubernetes = {
    config_path = "~/.kube/config"
  }
}

ephemeral "tunnel_kubernetes" "vault" {
  service_name = "vault"
  namespace    = "interview-test"
  target_port  = 8200
  kubernetes = {
    config_path = "~/.kube/config"
  }
}

provider "postgresql" {
  host            = "127.0.0.1"
  port            = ephemeral.tunnel_kubernetes.postgres.local_port
  username        = "postgres"
  password        = "postgres"
  sslmode         = "disable"
  connect_timeout = 15
  superuser       = false
}

provider "vault" {
  address = "http://127.0.0.1:${ephemeral.tunnel_kubernetes.vault.local_port}"
  token   = "root"
}

# Database Setup
resource "postgresql_database" "orders" {
  name = "orders"
}

resource "postgresql_database" "inventory" {
  name = "inventory"
}

# Vault Secrets
resource "vault_mount" "kv" {
  path = "secret"
  type = "kv-v2"
}

resource "vault_kv_secret_v2" "db_credentials" {
  mount = vault_mount.kv.path
  name  = "db-credentials"
  data_json = jsonencode({
    username = "postgres"
    password = "postgres"
  })
}
