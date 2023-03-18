/* Copyright © 2019 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: MPL-2.0 */

package nsxt

import (
	"fmt"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	gm_infra "github.com/vmware/vsphere-automation-sdk-go/services/nsxt-gm/global_infra"
	gm_tier1s "github.com/vmware/vsphere-automation-sdk-go/services/nsxt-gm/global_infra/tier_1s"
	gm_locale_services "github.com/vmware/vsphere-automation-sdk-go/services/nsxt-gm/global_infra/tier_1s/locale_services"
	gm_model "github.com/vmware/vsphere-automation-sdk-go/services/nsxt-gm/model"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/infra"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/infra/tier_1s/locale_services"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
	log "github.com/sourcegraph-ce/logrus"
	"strconv"
	"strings"
)

func resourceNsxtPolicyTier1GatewayInterface() *schema.Resource {
	return &schema.Resource{
		Create: resourceNsxtPolicyTier1GatewayInterfaceCreate,
		Read:   resourceNsxtPolicyTier1GatewayInterfaceRead,
		Update: resourceNsxtPolicyTier1GatewayInterfaceUpdate,
		Delete: resourceNsxtPolicyTier1GatewayInterfaceDelete,
		Importer: &schema.ResourceImporter{
			State: resourceNsxtPolicyTier1GatewayInterfaceImport,
		},

		Schema: map[string]*schema.Schema{
			"nsx_id":                 getNsxIDSchema(),
			"path":                   getPathSchema(),
			"display_name":           getDisplayNameSchema(),
			"description":            getDescriptionSchema(),
			"revision":               getRevisionSchema(),
			"tag":                    getTagsSchema(),
			"gateway_path":           getPolicyPathSchema(true, true, "Policy path for tier1 gateway"),
			"segment_path":           getPolicyPathSchema(true, true, "Policy path for connected segment"),
			"subnets":                getGatewayInterfaceSubnetsSchema(),
			"mtu":                    getMtuSchema(),
			"ipv6_ndra_profile_path": getIPv6NDRAPathSchema(),
			"urpf_mode":              getGatewayInterfaceUrpfModeSchema(),
			"locale_service_id": {
				Type:        schema.TypeString,
				Description: "Locale Service ID for this interface",
				Computed:    true,
			},
			"site_path": {
				Type:         schema.TypeString,
				Description:  "Path of the site the Tier1 edge cluster belongs to",
				Optional:     true,
				ForceNew:     true,
				ValidateFunc: validatePolicyPath(),
			},
		},
	}
}

func getGatewayInterfaceSubnetList(d *schema.ResourceData) []model.InterfaceSubnet {
	subnets := interface2StringList(d.Get("subnets").([]interface{}))
	var interfaceSubnetList []model.InterfaceSubnet
	for _, subnet := range subnets {
		result := strings.Split(subnet, "/")
		var ipAddresses []string
		ipAddresses = append(ipAddresses, result[0])
		prefix, _ := strconv.Atoi(result[1])
		prefix64 := int64(prefix)
		interfaceSubnet := model.InterfaceSubnet{
			IpAddresses: ipAddresses,
			PrefixLen:   &prefix64,
		}
		interfaceSubnetList = append(interfaceSubnetList, interfaceSubnet)
	}

	return interfaceSubnetList
}

func resourceNsxtPolicyTier1GatewayInterfaceCreate(d *schema.ResourceData, m interface{}) error {
	connector := getPolicyConnector(m)

	id := d.Get("nsx_id").(string)
	tier1Path := d.Get("gateway_path").(string)
	sitePath := d.Get("site_path").(string)
	tier1ID := getPolicyIDFromPath(tier1Path)
	localeServiceID := ""
	if isPolicyGlobalManager(m) {
		if sitePath == "" {
			return attributeRequiredGlobalManagerError("site_path", "nsxt_policy_tier1_gateway_interface")
		}
		localeServices, err := listPolicyTier1GatewayLocaleServices(connector, tier1ID, true)
		if err != nil {
			return err
		}
		localeServiceID, err = getGlobalPolicyGatewayLocaleServiceIDWithSite(localeServices, sitePath, tier1ID)
		if err != nil {
			return err
		}

	} else {
		if sitePath != "" {
			return globalManagerOnlyError()
		}
		localeService, err := getPolicyTier1GatewayLocaleServiceEntry(tier1ID, connector)
		if err != nil {
			return err
		}
		if localeService == nil {
			return fmt.Errorf("Edge cluster is mandatory on gateway %s in order to create interfaces", tier1ID)
		}
		localeServiceID = *localeService.Id
	}

	if id == "" {
		id = newUUID()
	} else {
		var err error
		if isPolicyGlobalManager(m) {
			client := gm_locale_services.NewDefaultInterfacesClient(connector)
			_, err = client.Get(tier1ID, localeServiceID, id)
		} else {
			client := locale_services.NewDefaultInterfacesClient(connector)
			_, err = client.Get(tier1ID, localeServiceID, id)
		}

		if err == nil {
			return fmt.Errorf("Interface with ID '%s' already exists on Tier1 Gateway %s", id, tier1ID)
		} else if !isNotFoundError(err) {
			return err
		}
	}

	displayName := d.Get("display_name").(string)
	description := d.Get("description").(string)
	segmentPath := d.Get("segment_path").(string)
	tags := getPolicyTagsFromSchema(d)
	interfaceSubnetList := getGatewayInterfaceSubnetList(d)
	var ipv6ProfilePaths []string
	if d.Get("ipv6_ndra_profile_path").(string) != "" {
		ipv6ProfilePaths = append(ipv6ProfilePaths, d.Get("ipv6_ndra_profile_path").(string))
	}
	mtu := int64(d.Get("mtu").(int))
	obj := model.Tier1Interface{
		Id:               &id,
		DisplayName:      &displayName,
		Description:      &description,
		Tags:             tags,
		Subnets:          interfaceSubnetList,
		SegmentPath:      &segmentPath,
		Ipv6ProfilePaths: ipv6ProfilePaths,
	}

	if mtu > 0 {
		obj.Mtu = &mtu
	}

	if nsxVersionHigherOrEqual("3.0.0") {
		urpfMode := d.Get("urpf_mode").(string)
		obj.UrpfMode = &urpfMode
	}

	// Create the resource using PATCH
	log.Printf("[INFO] Creating tier1 interface with ID %s", id)
	var err error
	if isPolicyGlobalManager(m) {
		gmObj, err := convertModelBindingType(obj, model.Tier1InterfaceBindingType(), gm_model.Tier1InterfaceBindingType())
		if err != nil {
			return err
		}
		client := gm_locale_services.NewDefaultInterfacesClient(connector)
		err = client.Patch(tier1ID, localeServiceID, id, gmObj.(gm_model.Tier1Interface))
	} else {
		client := locale_services.NewDefaultInterfacesClient(connector)
		err = client.Patch(tier1ID, localeServiceID, id, obj)
	}

	if err != nil {
		return handleCreateError("Tier1 Interface", id, err)
	}

	d.SetId(id)
	d.Set("nsx_id", id)
	d.Set("locale_service_id", localeServiceID)

	return resourceNsxtPolicyTier1GatewayInterfaceRead(d, m)
}

func resourceNsxtPolicyTier1GatewayInterfaceRead(d *schema.ResourceData, m interface{}) error {
	connector := getPolicyConnector(m)
	id := d.Id()
	tier1Path := d.Get("gateway_path").(string)
	localeServiceID := d.Get("locale_service_id").(string)
	tier1ID := getPolicyIDFromPath(tier1Path)
	if id == "" || tier1ID == "" {
		return fmt.Errorf("Error obtaining Tier1 Interface id")
	}

	var obj model.Tier1Interface
	if isPolicyGlobalManager(m) {
		client := gm_locale_services.NewDefaultInterfacesClient(connector)
		gmObj, err := client.Get(tier1ID, localeServiceID, id)
		if err != nil {
			return handleReadError(d, "Tier1 Interface", id, err)
		}
		lmObj, err1 := convertModelBindingType(gmObj, gm_model.Tier1InterfaceBindingType(), model.Tier1InterfaceBindingType())
		if err1 != nil {
			return err1
		}
		obj = lmObj.(model.Tier1Interface)
		tier1Client := gm_tier1s.NewDefaultLocaleServicesClient(connector)
		localeService, err := tier1Client.Get(tier1ID, localeServiceID)
		if err != nil {
			return err
		}
		sitePath := getSitePathFromEdgePath(*localeService.EdgeClusterPath)
		d.Set("site_path", sitePath)
	} else {
		var err error
		client := locale_services.NewDefaultInterfacesClient(connector)
		obj, err = client.Get(tier1ID, defaultPolicyLocaleServiceID, id)
		if err != nil {
			return handleReadError(d, "Tier1 Interface", id, err)
		}
	}

	d.Set("display_name", obj.DisplayName)
	d.Set("description", obj.Description)
	setPolicyTagsInSchema(d, obj.Tags)
	d.Set("nsx_id", id)
	d.Set("path", obj.Path)
	d.Set("revision", obj.Revision)
	d.Set("segment_path", obj.SegmentPath)
	if obj.Ipv6ProfilePaths != nil {
		d.Set("ipv6_ndra_profile_path", obj.Ipv6ProfilePaths[0]) // only one supported for now
	}
	d.Set("mtu", obj.Mtu)

	if obj.Subnets != nil {
		var subnetList []string
		for _, subnet := range obj.Subnets {
			cidr := fmt.Sprintf("%s/%d", subnet.IpAddresses[0], *subnet.PrefixLen)
			subnetList = append(subnetList, cidr)
		}
		d.Set("subnets", subnetList)
	}

	if obj.UrpfMode != nil {
		d.Set("urpf_mode", obj.UrpfMode)
	} else {
		// assign default for version that is lower than 3.0.0
		d.Set("urpf_mode", model.Tier0Interface_URPF_MODE_STRICT)
	}

	return nil
}

func resourceNsxtPolicyTier1GatewayInterfaceUpdate(d *schema.ResourceData, m interface{}) error {
	connector := getPolicyConnector(m)

	id := d.Id()
	tier1Path := d.Get("gateway_path").(string)
	localeServiceID := d.Get("locale_service_id").(string)
	tier1ID := getPolicyIDFromPath(tier1Path)
	if id == "" || tier1ID == "" || localeServiceID == "" {
		return fmt.Errorf("Error obtaining Tier1 id or Locale Service id")
	}

	displayName := d.Get("display_name").(string)
	description := d.Get("description").(string)
	tags := getPolicyTagsFromSchema(d)
	interfaceSubnetList := getGatewayInterfaceSubnetList(d)
	segmentPath := d.Get("segment_path").(string)
	var ipv6ProfilePaths []string
	if d.Get("ipv6_ndra_profile_path").(string) != "" {
		ipv6ProfilePaths = append(ipv6ProfilePaths, d.Get("ipv6_ndra_profile_path").(string))
	}
	mtu := int64(d.Get("mtu").(int))
	revision := int64(d.Get("revision").(int))
	obj := model.Tier1Interface{
		DisplayName:      &displayName,
		Description:      &description,
		Tags:             tags,
		Subnets:          interfaceSubnetList,
		SegmentPath:      &segmentPath,
		Ipv6ProfilePaths: ipv6ProfilePaths,
		Revision:         &revision,
	}

	if mtu > 0 {
		obj.Mtu = &mtu
	}

	if nsxVersionHigherOrEqual("3.0.0") {
		urpfMode := d.Get("urpf_mode").(string)
		obj.UrpfMode = &urpfMode
	}
	var err error
	if isPolicyGlobalManager(m) {
		gmObj, err1 := convertModelBindingType(obj, model.Tier1InterfaceBindingType(), gm_model.Tier1InterfaceBindingType())
		if err1 != nil {
			return err1
		}
		client := gm_locale_services.NewDefaultInterfacesClient(connector)
		_, err = client.Update(tier1ID, localeServiceID, id, gmObj.(gm_model.Tier1Interface))
	} else {
		client := locale_services.NewDefaultInterfacesClient(connector)
		_, err = client.Update(tier1ID, localeServiceID, id, obj)
	}
	if err != nil {
		return handleUpdateError("Tier1 Interface", id, err)
	}

	return resourceNsxtPolicyTier1GatewayInterfaceRead(d, m)
}

func resourceNsxtPolicyTier1GatewayInterfaceDelete(d *schema.ResourceData, m interface{}) error {
	connector := getPolicyConnector(m)

	id := d.Id()
	tier1Path := d.Get("gateway_path").(string)
	tier1ID := getPolicyIDFromPath(tier1Path)
	localeServiceID := d.Get("locale_service_id").(string)
	if id == "" || tier1ID == "" || localeServiceID == "" {
		return fmt.Errorf("Error obtaining Tier1 id or Locale Service id")
	}

	var err error
	if isPolicyGlobalManager(m) {
		client := gm_locale_services.NewDefaultInterfacesClient(connector)
		err = client.Delete(tier1ID, localeServiceID, id)
	} else {
		client := locale_services.NewDefaultInterfacesClient(connector)
		err = client.Delete(tier1ID, localeServiceID, id)
	}
	if err != nil {
		return handleDeleteError("Tier1 Interface", id, err)
	}

	return nil
}

func resourceNsxtPolicyTier1GatewayInterfaceImport(d *schema.ResourceData, m interface{}) ([]*schema.ResourceData, error) {
	importID := d.Id()
	s := strings.Split(importID, "/")
	if len(s) != 3 {
		return nil, fmt.Errorf("Please provide <gateway-id>/<locale-service-id>/<interface-id> as an input")
	}

	gwID := s[0]
	connector := getPolicyConnector(m)
	var tier1GW model.Tier1
	if isPolicyGlobalManager(m) {
		client := gm_infra.NewDefaultTier1sClient(connector)
		gmObj, err := client.Get(gwID)
		if err != nil {
			return nil, err
		}
		lmObj, err := convertModelBindingType(gmObj, gm_model.Tier1BindingType(), model.Tier1BindingType())
		if err != nil {
			return nil, err
		}
		tier1GW = lmObj.(model.Tier1)
	} else {
		var err error
		client := infra.NewDefaultTier1sClient(connector)
		tier1GW, err = client.Get(gwID)
		if err != nil {
			return nil, err
		}
	}
	d.Set("gateway_path", tier1GW.Path)
	d.Set("locale_service_id", s[1])

	d.SetId(s[2])

	return []*schema.ResourceData{d}, nil

}
