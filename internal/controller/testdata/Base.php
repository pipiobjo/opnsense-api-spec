<?php
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
