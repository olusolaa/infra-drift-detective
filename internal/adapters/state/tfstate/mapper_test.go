package tfstate

import (
	"encoding/json"
	"testing"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	portsmocks "github.com/olusolaa/infra-drift-detector/internal/core/ports/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestMapRawInstanceToDomain(t *testing.T) {
	log := portsmocks.NewLogger(t)
	log.On("WithFields", mock.Anything).Maybe().Return(log)
	log.On("Debugf", mock.Anything, mock.Anything).Maybe().Return()

	t.Run("EC2 instance", func(t *testing.T) {
		j := `{
		  "mode":"managed",
		  "type":"aws_instance",
		  "name":"web",
		  "provider":"registry.terraform.io/hashicorp/aws",
		  "instances":[{
		    "schema_version":1,
		    "attributes":{
		      "id":"i-123abc456def",
		      "ami":"ami-abc",
		      "instance_type":"t2.small",
		      "tags":{"Name":"MappedInstance","Env":"Test"}
		    }
		  }]
		}`
		var r Resource
		require.NoError(t, json.Unmarshal([]byte(j), &r))

		out, err := mapRawInstanceToDomain(&r, &r.Instances[0], log)
		require.NoError(t, err)

		meta := out.Metadata()
		assert.Equal(t, domain.KindComputeInstance, meta.Kind)
		assert.Equal(t, "aws_instance.web", meta.SourceIdentifier)
		assert.Equal(t, "i-123abc456def", meta.ProviderAssignedID)
		assert.Equal(t, "aws", meta.ProviderType)

		a := out.Attributes()
		assert.Equal(t, "i-123abc456def", a[domain.KeyID])
		assert.Equal(t, "t2.small", a[domain.ComputeInstanceTypeKey])
		assert.Equal(t, "ami-abc", a[domain.ComputeImageIDKey])
		assert.Equal(t, "MappedInstance", a[domain.KeyName])
		assert.Equal(t, map[string]string{"Name": "MappedInstance", "Env": "Test"}, a[domain.KeyTags])
	})

	t.Run("S3 bucket", func(t *testing.T) {
		j := `{
		  "mode":"managed",
		  "type":"aws_s3_bucket",
		  "name":"logs",
		  "provider":"registry.terraform.io/hashicorp/aws",
		  "instances":[{
		    "schema_version":0,
		    "attributes":{
		      "id":"my-log-bucket",
		      "bucket":"my-log-bucket",
		      "acl":"private",
		      "versioning":[{"enabled":true}]
		    }
		  }]
		}`
		var r Resource
		require.NoError(t, json.Unmarshal([]byte(j), &r))

		out, err := mapRawInstanceToDomain(&r, &r.Instances[0], log)
		require.NoError(t, err)

		meta := out.Metadata()
		assert.Equal(t, domain.KindStorageBucket, meta.Kind)
		assert.Equal(t, "aws_s3_bucket.logs", meta.SourceIdentifier)
		assert.Equal(t, "my-log-bucket", meta.ProviderAssignedID)

		a := out.Attributes()
		assert.Equal(t, "my-log-bucket", a[domain.KeyID])
		assert.Equal(t, "private", a[domain.StorageBucketACLKey])
		assert.Equal(t, true, a[domain.StorageBucketVersioningKey])
		assert.Equal(t, "my-log-bucket", a[domain.KeyName])
	})

	t.Run("nil input", func(t *testing.T) {
		_, err := mapRawInstanceToDomain(nil, nil, log)
		assert.Error(t, err)
	})

	t.Run("unsupported type", func(t *testing.T) {
		r := &Resource{
			Mode:     "managed",
			Type:     "aws_vpc",
			Name:     "main",
			Provider: "registry.terraform.io/hashicorp/aws",
			Instances: []Instance{{
				Attributes: map[string]any{"id": "vpc-123"},
			}},
		}
		_, err := mapRawInstanceToDomain(r, &r.Instances[0], log)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported")
	})

	t.Run("nil attributes", func(t *testing.T) {
		r := &Resource{
			Mode:     "managed",
			Type:     "aws_instance",
			Name:     "test",
			Provider: "registry.terraform.io/hashicorp/aws",
			Instances: []Instance{{
				Attributes: nil,
			}},
		}
		out, err := mapRawInstanceToDomain(r, &r.Instances[0], log)
		require.NoError(t, err)
		assert.NotNil(t, out)
		assert.Empty(t, out.Attributes())
	})
}

func TestMapProviderToType(t *testing.T) {
	cases := []struct {
		addr   string
		want   string
		hasErr bool
	}{
		{"registry.terraform.io/hashicorp/aws", "aws", false},
		{"registry.terraform.io/community/google", "google", false},
		{"aws", "aws", false},
		{"", "unknown", true},
		{"registry.terraform.io//aws", "aws", false},
		{"registry.terraform.io///", "unknown", true},
	}

	for _, c := range cases {
		got, err := mapProviderToType(c.addr)
		assert.Equal(t, c.want, got)
		if c.hasErr {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
		}
	}
}
