<?php
/*
 * Copyright (C) 2026 opnsense-api-spec contributors
 * SPDX-License-Identifier: BSD-2-Clause
 */
namespace OPNsense\Demo\FieldTypes;

use OPNsense\Base\FieldTypes\DemoRangeField;

/* adds a check of its own to its parent's */
class AddingField extends DemoRangeField
{
    public function getValidators()
    {
        $validators = parent::getValidators();
        $validators[] = new \stdClass();
        return $validators;
    }
}

/* replaces its parent's checks */
class ReplacingField extends DemoRangeField
{
    public function getValidators()
    {
        return [];
    }
}
