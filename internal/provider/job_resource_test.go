package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestParseHttpResponse(t *testing.T) {
	templateID := basetypes.NewInt64Value(1)
	inventoryID := basetypes.NewInt64Value(2)
	extraVars := basetypes.NewStringNull()
	testTable := []struct {
		name     string
		body     []byte
		expected jobResourceModel
		failure  bool
	}{
		{
			name:    "no ignored fields",
			failure: false,
			body:    []byte(`{"job_type": "run", "url": "/api/v2/jobs/14/", "status": "pending"}`),
			expected: jobResourceModel{
				TemplateID:    templateID,
				Type:          types.StringValue("run"),
				URL:           types.StringValue("/api/v2/jobs/14/"),
				Status:        types.StringValue("pending"),
				InventoryID:   inventoryID,
				ExtraVars:     extraVars,
				IgnoredFields: types.ListNull(types.StringType),
			},
		},
		{
			name:    "ignored fields",
			failure: false,
			body: []byte(`{"job_type": "run", "url": "/api/v2/jobs/14/", "status": 
			"pending", "ignored_fields": {"extra_vars": "{\"bucket_state\":\"absent\"}"}}`),
			expected: jobResourceModel{
				TemplateID:    templateID,
				Type:          types.StringValue("run"),
				URL:           types.StringValue("/api/v2/jobs/14/"),
				Status:        types.StringValue("pending"),
				InventoryID:   inventoryID,
				ExtraVars:     extraVars,
				IgnoredFields: basetypes.NewListValueMust(types.StringType, []attr.Value{types.StringValue("extra_vars")}),
			},
		},
		{
			name:     "bad json",
			failure:  true,
			body:     []byte(`{job_type: run}`),
			expected: jobResourceModel{},
		},
	}
	for _, tc := range testTable {
		t.Run(tc.name, func(t *testing.T) {
			d := jobResourceModel{
				TemplateID:  templateID,
				InventoryID: inventoryID,
				ExtraVars:   extraVars,
			}
			err := d.ParseHTTPResponse(tc.body)
			if tc.failure {
				if err == nil {
					t.Errorf("expecting failure while the process has not failed")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected process failure (%s)", err.Error())
				} else if !reflect.DeepEqual(tc.expected, d) {
					t.Errorf("expected (%v) - result (%v)", tc.expected, d)
				}
			}
		})
	}
}

func toString(b io.Reader) string {
	if b == nil {
		return ""
	}
	buf := new(strings.Builder)
	_, err := io.Copy(buf, b)
	if err != nil {
		return ""
	}
	return buf.String()
}

func toJSON(b io.Reader) map[string]interface{} {
	var result map[string]interface{}
	err := json.Unmarshal([]byte(toString(b)), &result)
	if err != nil {
		return make(map[string]interface{})
	}
	return result
}

func TestCreateRequestBody(t *testing.T) {
	testTable := []struct {
		name     string
		input    jobResourceModel
		expected *bytes.Reader
	}{
		{
			name: "unknown fields",
			input: jobResourceModel{
				ExtraVars:   basetypes.NewStringUnknown(),
				InventoryID: basetypes.NewInt64Unknown(),
			},
			expected: nil,
		},
		{
			name: "null fields",
			input: jobResourceModel{
				ExtraVars:   basetypes.NewStringNull(),
				InventoryID: basetypes.NewInt64Null(),
			},
			expected: nil,
		},
		{
			name: "extra vars only",
			input: jobResourceModel{
				ExtraVars:   types.StringValue("{\"test_name\":\"extra_vars\", \"provider\":\"aap\"}"),
				InventoryID: basetypes.NewInt64Null(),
			},
			expected: bytes.NewReader([]byte(`{"extra_vars":{"test_name":"extra_vars","provider":"aap"}}`)),
		},
		{
			name: "inventory vars only",
			input: jobResourceModel{
				ExtraVars:   basetypes.NewStringNull(),
				InventoryID: basetypes.NewInt64Value(201),
			},
			expected: bytes.NewReader([]byte(`{"inventory": 201}`)),
		},
		{
			name: "combined",
			input: jobResourceModel{
				ExtraVars:   types.StringValue("{\"test_name\":\"extra_vars\", \"provider\":\"aap\"}"),
				InventoryID: basetypes.NewInt64Value(3),
			},
			expected: bytes.NewReader([]byte(
				`{"inventory": 3, "extra_vars":{"test_name":"extra_vars","provider":"aap"}}`)),
		},
	}

	for _, tc := range testTable {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := tc.input.CreateRequestBody()
			if tc.expected == nil && data != nil {
				t.Errorf("expected nil but result is not nil")
			}
			if tc.expected == nil && data != nil {
				t.Errorf("expected result not nil but result is nil")
			}
			if tc.expected != nil && !reflect.DeepEqual(toJSON(tc.expected), toJSON(data)) {
				t.Errorf("expected (%s)", toString(tc.expected))
				t.Errorf("computed (%s)", toString(data))
			}
		})
	}
}

type MockJobResource struct {
	ID        string
	URL       string
	Inventory string
	Response  map[string]string
}

func NewMockJobResource(id, inventory, url string) *MockJobResource {
	return &MockJobResource{
		ID:        id,
		URL:       url,
		Inventory: inventory,
		Response:  map[string]string{},
	}
}

func (d *MockJobResource) GetTemplateID() string {
	return d.ID
}

func (d *MockJobResource) GetURL() string {
	return d.URL
}

func (d *MockJobResource) ParseHTTPResponse(body []byte) error {
	err := json.Unmarshal(body, &d.Response)
	if err != nil {
		return err
	}
	return nil
}

func (d *MockJobResource) CreateRequestBody() (io.Reader, error) {
	if len(d.Inventory) == 0 {
		return nil, nil
	}
	m := map[string]string{"Inventory": d.Inventory}
	jsonRaw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(jsonRaw), nil
}

type MockHTTPClient struct {
	acceptMethods []string
	httpCode      int
}

func NewMockHTTPClient(methods []string, httpCode int) *MockHTTPClient {
	return &MockHTTPClient{
		acceptMethods: methods,
		httpCode:      httpCode,
	}
}

func mergeStringMaps(m1 map[string]string, m2 map[string]string) map[string]string {
	merged := make(map[string]string)
	for k, v := range m1 {
		merged[k] = v
	}
	for k, v := range m2 {
		merged[k] = v
	}
	return merged
}

var mResponse1 = map[string]string{
	"status": "running",
	"type":   "check",
}

var mResponse2 = map[string]string{
	"status":   "pending",
	"playbook": "ansible_aws.yaml",
}

var mResponse3 = map[string]string{
	"status":                "complete",
	"execution_environment": "3",
}

func (c *MockHTTPClient) doRequest(method string, path string, data io.Reader) (*http.Response, []byte, error) {
	config := map[string]map[string]string{
		"/api/v2/job_templates/1/launch/": mResponse1,
		"/api/v2/job_templates/2/launch/": mResponse2,
		"/api/v2/jobs/1/":                 mResponse1,
		"/api/v2/jobs/2/":                 mResponse3,
	}

	if !slices.Contains(c.acceptMethods, method) {
		return nil, nil, nil
	}
	response, ok := config[path]
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound}, nil, nil
	}
	if data != nil {
		// add request info into response
		buf := new(strings.Builder)
		_, err := io.Copy(buf, data)
		if err != nil {
			return nil, nil, err
		}
		var mData map[string]string
		err = json.Unmarshal([]byte(buf.String()), &mData)
		if err != nil {
			return nil, nil, err
		}
		response = mergeStringMaps(response, mData)
	}
	result, err := json.Marshal(response)
	if err != nil {
		return nil, nil, err
	}
	return &http.Response{StatusCode: c.httpCode}, result, nil
}

func TestCreateJob(t *testing.T) {
	testTable := []struct {
		name          string
		ID            string
		Inventory     string
		expected      map[string]string
		acceptMethods []string
		httpCode      int
		failed        bool
	}{
		{
			name:          "create job simple job (no request data)",
			ID:            "1",
			Inventory:     "",
			httpCode:      http.StatusCreated,
			failed:        false,
			acceptMethods: []string{"POST", "post"},
			expected:      mResponse1,
		},
		{
			name:          "create job with request data",
			ID:            "1",
			Inventory:     "3",
			httpCode:      http.StatusCreated,
			failed:        false,
			acceptMethods: []string{"POST", "post"},
			expected:      mergeStringMaps(mResponse1, map[string]string{"Inventory": "3"}),
		},
		{
			name:          "try with non existing template id",
			ID:            "-1",
			Inventory:     "3",
			httpCode:      http.StatusCreated,
			failed:        true,
			acceptMethods: []string{"POST", "post"},
			expected:      nil,
		},
		{
			name:          "Unexpected method leading to not found",
			ID:            "1",
			Inventory:     "3",
			httpCode:      http.StatusCreated,
			failed:        true,
			acceptMethods: []string{"GET", "get"},
			expected:      nil,
		},
		{
			name:          "using another template id",
			ID:            "2",
			Inventory:     "1",
			httpCode:      http.StatusCreated,
			failed:        false,
			acceptMethods: []string{"POST", "post"},
			expected:      mergeStringMaps(mResponse2, map[string]string{"Inventory": "1"}),
		},
	}

	for _, tc := range testTable {
		t.Run(tc.name, func(t *testing.T) {
			resource := NewMockJobResource(tc.ID, tc.Inventory, "")

			job := JobResource{
				client: NewMockHTTPClient(tc.acceptMethods, tc.httpCode),
			}
			err := job.CreateJob(resource)
			if (tc.failed && err == nil) || (!tc.failed && err != nil) {
				if err != nil {
					t.Errorf("process has failed with (%s) while it should not", err.Error())
				} else {
					t.Errorf("failure expected but the process did not failed!!")
				}
			} else if !tc.failed && !reflect.DeepEqual(tc.expected, resource.Response) {
				t.Errorf("expected (%v)", tc.expected)
				t.Errorf("computed (%v)", resource.Response)
			}
		})
	}
}

func TestReadJob(t *testing.T) {
	testTable := []struct {
		name          string
		url           string
		expected      map[string]string
		acceptMethods []string
		httpCode      int
		failed        bool
	}{
		{
			name:          "Read existing job",
			url:           "/api/v2/jobs/1/",
			httpCode:      http.StatusOK,
			failed:        false,
			acceptMethods: []string{"GET", "get"},
			expected:      mResponse1,
		},
		{
			name:          "Read another job",
			url:           "/api/v2/jobs/2/",
			httpCode:      http.StatusOK,
			failed:        false,
			acceptMethods: []string{"GET", "get"},
			expected:      mResponse3,
		},
		{
			name:          "GET not part of accepted methods",
			url:           "/api/v2/jobs/2/",
			httpCode:      http.StatusOK,
			failed:        true,
			acceptMethods: []string{"HEAD"},
			expected:      nil,
		},
		{
			name:          "no url provided",
			url:           "",
			httpCode:      http.StatusOK,
			failed:        false,
			acceptMethods: []string{"GET", "get"},
			expected:      map[string]string{},
		},
	}

	for _, tc := range testTable {
		t.Run(tc.name, func(t *testing.T) {
			resource := NewMockJobResource("", "", tc.url)

			job := JobResource{
				client: NewMockHTTPClient(tc.acceptMethods, tc.httpCode),
			}
			err := job.ReadJob(resource)
			if (tc.failed && err == nil) || (!tc.failed && err != nil) {
				if err != nil {
					t.Errorf("process has failed with (%s) while it should not", err.Error())
				} else {
					t.Errorf("failure expected but the process did not failed!!")
				}
			} else if !tc.failed && !reflect.DeepEqual(tc.expected, resource.Response) {
				t.Errorf("expected (%v)", tc.expected)
				t.Errorf("computed (%v)", resource.Response)
			}
		})
	}
}

// Acceptance tests

func getJobResourceFromStateFile(s *terraform.State) (map[string]interface{}, error) {
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "aap_job" {
			continue
		}
		jobURL := rs.Primary.Attributes["job_url"]
		return testGetResource(jobURL)
	}
	return nil, fmt.Errorf("Job resource not found from state file")
}

func testAccCheckJobExists(s *terraform.State) error {
	_, err := getJobResourceFromStateFile(s)
	return err
}

func testAccCheckJobUpdate(urlBefore *string, shouldDiffer bool) func(s *terraform.State) error {
	return func(s *terraform.State) error {
		var jobURL string
		for _, rs := range s.RootModule().Resources {
			if rs.Type != "aap_job" {
				continue
			}
			jobURL = rs.Primary.Attributes["job_url"]
		}
		if len(jobURL) == 0 {
			return fmt.Errorf("Job resource not found from state file")
		}
		if len(*urlBefore) == 0 {
			*urlBefore = jobURL
			return nil
		}
		if jobURL == *urlBefore && shouldDiffer {
			return fmt.Errorf("Job resource URLs are equal while expecting them to differ. Before [%s] After [%s]", *urlBefore, jobURL)
		} else if jobURL != *urlBefore && !shouldDiffer {
			return fmt.Errorf("Job resource URLs differ while expecting them to be equals. Before [%s] After [%s]", *urlBefore, jobURL)
		}
		return nil
	}
}

func testAccJobResourcePreCheck(t *testing.T) {
	// ensure provider requirements
	testAccPreCheck(t)

	requiredAAPJobEnvVars := []string{
		"AAP_TEST_JOB_TEMPLATE_ID",
		"AAP_TEST_JOB_INVENTORY_ID",
	}

	for _, key := range requiredAAPJobEnvVars {
		if v := os.Getenv(key); v == "" {
			t.Fatalf("'%s' environment variable must be set when running acceptance tests for job resource", key)
		}
	}
}

const resourceName = "aap_job.test"

func TestAccAAPJob_basic(t *testing.T) {
	jobTemplateID := os.Getenv("AAP_TEST_JOB_TEMPLATE_ID")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccJobResourcePreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccBasicJob(jobTemplateID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(resourceName, "status", regexp.MustCompile("^(failed|pending|running|complete|successful|waiting)$")),
					resource.TestMatchResourceAttr(resourceName, "job_type", regexp.MustCompile("^(run|check)$")),
					resource.TestMatchResourceAttr(resourceName, "job_url", regexp.MustCompile("^/api/v2/jobs/[0-9]*/$")),
					testAccCheckJobExists,
				),
			},
		},
	})
}

func TestAccAAPJob_UpdateWithSameParameters(t *testing.T) {
	var jobURLBefore string

	jobTemplateID := os.Getenv("AAP_TEST_JOB_TEMPLATE_ID")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccJobResourcePreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccBasicJob(jobTemplateID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(resourceName, "status", regexp.MustCompile("^(failed|pending|running|complete|successful|waiting)$")),
					resource.TestMatchResourceAttr(resourceName, "job_type", regexp.MustCompile("^(run|check)$")),
					resource.TestMatchResourceAttr(resourceName, "job_url", regexp.MustCompile("^/api/v2/jobs/[0-9]*/$")),
					testAccCheckJobUpdate(&jobURLBefore, false),
				),
			},
			{
				Config: testAccBasicJob(jobTemplateID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(resourceName, "status", regexp.MustCompile("^(failed|pending|running|complete|successful|waiting)$")),
					resource.TestMatchResourceAttr(resourceName, "job_type", regexp.MustCompile("^(run|check)$")),
					resource.TestMatchResourceAttr(resourceName, "job_url", regexp.MustCompile("^/api/v2/jobs/[0-9]*/$")),
					testAccCheckJobUpdate(&jobURLBefore, false),
				),
			},
		},
	})
}

func TestAccAAPJob_UpdateWithNewInventoryId(t *testing.T) {
	var jobURLBefore string

	jobTemplateID := os.Getenv("AAP_TEST_JOB_TEMPLATE_ID")
	inventoryID := os.Getenv("AAP_TEST_JOB_INVENTORY_ID")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccJobResourcePreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create and Read testing
			{
				Config: testAccBasicJob(jobTemplateID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(resourceName, "status", regexp.MustCompile("^(failed|pending|running|complete|successful|waiting)$")),
					resource.TestMatchResourceAttr(resourceName, "job_type", regexp.MustCompile("^(run|check)$")),
					resource.TestMatchResourceAttr(resourceName, "job_url", regexp.MustCompile("^/api/v2/jobs/[0-9]*/$")),
					testAccCheckJobUpdate(&jobURLBefore, false),
				),
			},
			{
				Config: testAccUpdateJob(jobTemplateID, inventoryID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(resourceName, "status", regexp.MustCompile("^(failed|pending|running|complete|successful|waiting)$")),
					resource.TestMatchResourceAttr(resourceName, "job_type", regexp.MustCompile("^(run|check)$")),
					resource.TestMatchResourceAttr(resourceName, "job_url", regexp.MustCompile("^/api/v2/jobs/[0-9]*/$")),
					testAccCheckJobUpdate(&jobURLBefore, true),
				),
			},
		},
	})
}

func testAccBasicJob(jobTemplateID string) string {
	return fmt.Sprintf(`
resource "aap_job" "test" {
	job_template_id   = %s
}
`, jobTemplateID)
}

func testAccUpdateJob(jobTemplateID, inventoryID string) string {
	return fmt.Sprintf(`
resource "aap_job" "test" {
	job_template_id   = %s
	inventory_id = %s
}
`, jobTemplateID, inventoryID)
}
