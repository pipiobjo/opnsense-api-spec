<?php
/*
 * Copyright (C) 2015-2026 Deciso B.V.
 * Copyright (C) 2016 IT-assistans Sverige AB
 * Copyright (C) 2017-2024 Franco Fichtner <franco@opnsense.org>
 * Copyright (C) 2018 Fabian Franz
 * Copyright (C) 2026 opnsense-api-spec contributors (shortened and modified for tests)
 * All rights reserved.
 *
 * Derived from opnsense/core src/opnsense/mvc/app/controllers/OPNsense/Base/ApiMutableModelControllerBase.php and ApiMutableServiceControllerBase.php.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions are met:
 *
 * 1. Redistributions of source code must retain the above copyright notice,
 *    this list of conditions and the following disclaimer.
 *
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED ``AS IS'' AND ANY EXPRESS OR IMPLIED WARRANTIES,
 * INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY
 * AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE
 * AUTHOR BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY,
 * OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
 * SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
 * INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
 * CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
 * ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
 * POSSIBILITY OF SUCH DAMAGE.
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
namespace OPNsense\Base;

abstract class ApiMutableModelControllerBase extends ApiControllerBase
{
    public function getAction()
    {
        if ($this->request->isGet()) {
            return [];
        }
    }

    public function setAction()
    {
        if ($this->request->isPost()) {
            return [];
        }
    }
}

abstract class ApiMutableServiceControllerBase extends ApiControllerBase
{
    public function reconfigureAction()
    {
        if ($this->request->isPost()) {
            return [];
        }
    }

    public function statusAction()
    {
        return [];
    }
}

class ApiControllerBase extends \OPNsense\Mvc\Controller
{
}
