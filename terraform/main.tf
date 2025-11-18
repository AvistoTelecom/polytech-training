# ============================================
# 0. RANDOM STRING
# ============================================
resource "random_string" "redis_suffix" {
  length  = 5
  upper   = false
  lower   = true
  numeric = true
  special = false
}

# ============================================
# 1. RESOURCE GROUP
# ============================================
resource "azurerm_resource_group" "rg" {
  location = var.resource_group_location
  name     = "rg-terraform"
}

# ============================================
# 2. VNET AND SUBNETS
# ============================================
resource "azurerm_virtual_network" "vnet" {
  name                = "vnet"
  location            = azurerm_resource_group.rg.location
  resource_group_name = azurerm_resource_group.rg.name
  address_space       = ["10.0.0.0/16"]
}

resource "azurerm_subnet" "platform" {
  name                 = "snet-platform"
  resource_group_name  = azurerm_resource_group.rg.name
  virtual_network_name = azurerm_virtual_network.vnet.name
  address_prefixes     = ["10.0.1.0/24"]
}

resource "azurerm_subnet" "asp" {
  name                 = "snet-asp"
  resource_group_name  = azurerm_resource_group.rg.name
  virtual_network_name = azurerm_virtual_network.vnet.name
  address_prefixes     = ["10.0.2.0/24"]

  # Required for Web App regional VNET integration
  delegation {
    name = "delegation-appservice"
    service_delegation {
      name = "Microsoft.Web/serverFarms"
      actions = [
        "Microsoft.Network/virtualNetworks/subnets/action"
      ]
    }
  }
}

# ============================================
# 3. REDIS CACHE (BASIC)
# ============================================
resource "azurerm_redis_cache" "redis" {
  name                = "redis-${random_string.redis_suffix.result}"
  location            = azurerm_resource_group.rg.location
  resource_group_name = azurerm_resource_group.rg.name

  capacity = 0
  family   = "C"
  sku_name = "Basic"

  minimum_tls_version = "1.2"
}

# ============================================
# 4. PRIVATE ENDPOINT FOR REDIS
# ============================================
resource "azurerm_private_endpoint" "redis_pe" {
  name                = "pe-redis"
  location            = azurerm_resource_group.rg.location
  resource_group_name = azurerm_resource_group.rg.name
  subnet_id           = azurerm_subnet.platform.id

  private_service_connection {
    name                           = "redis-privatelink"
    private_connection_resource_id = azurerm_redis_cache.redis.id
    subresource_names              = ["redisCache"]
    is_manual_connection           = false
  }
}

resource "azurerm_private_dns_zone" "redis_dns" {
  name                = "privatelink.redis.cache.windows.net"
  resource_group_name = azurerm_resource_group.rg.name
}

resource "azurerm_private_dns_zone_virtual_network_link" "redis_dns_link" {
  name                  = "redis-dns-link"
  resource_group_name   = azurerm_resource_group.rg.name
  private_dns_zone_name = azurerm_private_dns_zone.redis_dns.name
  virtual_network_id    = azurerm_virtual_network.vnet.id
}

resource "azurerm_private_dns_a_record" "redis_record" {
  name                = azurerm_redis_cache.redis.hostname
  zone_name           = azurerm_private_dns_zone.redis_dns.name
  resource_group_name = azurerm_resource_group.rg.name
  ttl                 = 300
  records             = [azurerm_private_endpoint.redis_pe.private_service_connection[0].private_ip_address]
}

# ============================================
# 5. APP SERVICE PLAN
# ============================================
resource "azurerm_service_plan" "voting_app" {
  name                = "asp"
  location            = azurerm_resource_group.rg.location
  resource_group_name = azurerm_resource_group.rg.name
  os_type             = "Linux"
  sku_name            = "B1"
}

# ============================================
# 6. WEB APP
# ============================================
resource "azurerm_linux_web_app" "vote" {
  name                = "vote"
  location            = azurerm_resource_group.rg.location
  resource_group_name = azurerm_resource_group.rg.name
  service_plan_id     = azurerm_service_plan.voting_app.id

  site_config {
    application_stack {
      docker_registry_url = var.registry_url
      docker_image_name   = var.web_app_vote_docker_image_name
    }
  }

  app_settings = {
    "REDIS_CONNECTION_STRING"               = "rediss://:${azurerm_redis_cache.redis.primary_access_key}@${azurerm_redis_cache.redis.hostname}:6380/0"
    "WEBSITES_ENABLE_APP_SERVICE_STORAGE"   = "false"
  }
}

# ============================================
# 7. WEB APP VNET INTEGRATION
# ============================================
resource "azurerm_app_service_virtual_network_swift_connection" "vnet_integration" {
  app_service_id = azurerm_linux_web_app.vote.id
  subnet_id      = azurerm_subnet.asp.id
}
